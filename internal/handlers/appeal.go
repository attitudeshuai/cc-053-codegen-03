package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"cc-053/internal/models"
	"cc-053/internal/repository"
)

type AppealHandler struct {
	appealRepo      *repository.AppealRepo
	recordingRepo   *repository.RecordingRepo
	slaHours        int
	maxPerRecording int
}

func NewAppealHandler(appealRepo *repository.AppealRepo, recordingRepo *repository.RecordingRepo, slaHours int, maxPerRecording int) *AppealHandler {
	return &AppealHandler{
		appealRepo:      appealRepo,
		recordingRepo:   recordingRepo,
		slaHours:        slaHours,
		maxPerRecording: maxPerRecording,
	}
}

// Submit 提交申诉：POST /api/v1/recordings/:id/appeals
// 仅「已退回」的录音可申诉；同一录音有在办申诉或累计次数达上限时不再受理。
func (h *AppealHandler) Submit(c *gin.Context) {
	recordingID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid recording id"})
		return
	}

	var req models.CreateAppealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	recording, err := h.recordingRepo.GetByID(recordingID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "recording not found"})
		return
	}
	if recording.Status != "rejected" {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "only rejected recordings can be appealed"})
		return
	}

	count, err := h.appealRepo.CountByRecording(recordingID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to count appeals", Detail: err.Error()})
		return
	}
	if count >= h.maxPerRecording {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: fmt.Sprintf("appeal limit reached for this recording (%d/%d), no longer accepted", count, h.maxPerRecording)})
		return
	}

	pending, err := h.appealRepo.HasPendingByRecording(recordingID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to check pending appeals", Detail: err.Error()})
		return
	}
	if pending {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "an appeal is already pending for this recording"})
		return
	}

	appeal := &models.Appeal{
		RecordingID:          recordingID,
		Appellant:            req.Appellant,
		Reason:               req.Reason,
		Status:               "pending",
		OriginalRejectedBy:   recording.RejectedBy,   // 快照：复检回避以申诉时点的结论人是谁为准
		OriginalRejectReason: recording.RejectReason, // 快照：被申诉的退回结论
		DeadlineAt:           time.Now().Add(time.Duration(h.slaHours) * time.Hour),
	}

	if err := h.appealRepo.Create(appeal); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to create appeal", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusCreated, models.APIResponse{Code: 201, Message: "appeal submitted", Data: appeal})
}

// List 申诉台账：GET /api/v1/appeals?recording_id=&status=&overdue=true
// 每条带 is_overdue，超期未办的排最前；overdue_count 为当前全量超期数，一眼可见。
func (h *AppealHandler) List(c *gin.Context) {
	var query models.AppealQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid query", Detail: err.Error()})
		return
	}
	query.Normalize()
	if query.Status != "" && query.Status != "pending" && query.Status != "upheld" && query.Status != "overturned" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid status filter"})
		return
	}

	appeals, total, err := h.appealRepo.List(query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list appeals", Detail: err.Error()})
		return
	}

	overdueCount, err := h.appealRepo.CountOverdue()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to count overdue appeals", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{
		"items":         appeals,
		"total":         total,
		"overdue_count": overdueCount,
		"offset":        query.Offset,
		"limit":         query.Limit,
	}})
}

// GetByID 申诉详情（含全部处理留痕）：GET /api/v1/appeals/:id
func (h *AppealHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid appeal id"})
		return
	}

	appeal, err := h.appealRepo.GetByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "appeal not found"})
		return
	}

	events, err := h.appealRepo.ListEvents(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list appeal events", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{"appeal": appeal, "events": events}})
}

// Resolve 复检处理：POST /api/v1/appeals/:id/resolve
// 复检人不能是当初下退回结论的人；维持/推翻都必须写依据；
// 推翻后录音恢复到退回前状态继续往下走。
func (h *AppealHandler) Resolve(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid appeal id"})
		return
	}

	var req models.ResolveAppealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	appeal, err := h.appealRepo.GetByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "appeal not found"})
		return
	}
	if appeal.Status != "pending" {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "appeal already resolved"})
		return
	}

	// 回避：复检人不得是当初下退回结论的人
	if appeal.OriginalRejectedBy != "" && strings.EqualFold(strings.TrimSpace(req.Reviewer), strings.TrimSpace(appeal.OriginalRejectedBy)) {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "reviewer must not be the one who made the original rejection"})
		return
	}

	appeal.Status = req.Decision
	appeal.Reviewer = req.Reviewer
	appeal.DecisionBasis = req.Basis

	restoreStatus := ""
	eventDetail := req.Basis
	if req.Decision == "overturned" {
		recording, err := h.recordingRepo.GetByID(appeal.RecordingID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to load recording", Detail: err.Error()})
			return
		}
		restoreStatus = recording.PreRejectStatus
		if restoreStatus == "" || restoreStatus == "rejected" {
			restoreStatus = "pending" // 老数据无快照时回到待处理，重新进入流水线
		}
		eventDetail = fmt.Sprintf("%s（录音已恢复至退回前状态：%s）", req.Basis, restoreStatus)
	}

	if err := h.appealRepo.Resolve(appeal, restoreStatus, eventDetail); err != nil {
		if errors.Is(err, repository.ErrAppealNotPending) {
			c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "appeal already resolved"})
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to resolve appeal", Detail: err.Error()})
		return
	}

	resolved, err := h.appealRepo.GetByID(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to reload appeal", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "appeal resolved", Data: resolved})
}
