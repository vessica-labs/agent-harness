package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vessica-labs/agent-harness/cloud-runner/internal/linear"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/model"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/providers/linearapi"
	"github.com/vessica-labs/agent-harness/cloud-runner/internal/store"
)

func decodeInputRequestEvent(runID, stage string, raw json.RawMessage) (model.InputRequest, error) {
	if stage != "product" && stage != "arch" {
		return model.InputRequest{}, fmt.Errorf("stage %q is not permitted to request human input", stage)
	}
	var envelope struct {
		Summary      string                `json:"summary"`
		Questions    []model.InputQuestion `json:"questions"`
		InputRequest *struct {
			Summary   string                `json:"summary"`
			Questions []model.InputQuestion `json:"questions"`
		} `json:"input_request"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return model.InputRequest{}, errors.New("invalid human input request payload")
	}
	if envelope.InputRequest != nil {
		envelope.Summary, envelope.Questions = envelope.InputRequest.Summary, envelope.InputRequest.Questions
	}
	request := model.InputRequest{RunID: runID, Stage: stage, Round: 1,
		Summary: strings.TrimSpace(envelope.Summary), Questions: envelope.Questions}
	if request.Summary == "" || len(request.Questions) == 0 || len(request.Questions) > 3 {
		return request, errors.New("human input request requires a summary and one to three questions")
	}
	seen := map[string]bool{}
	for index := range request.Questions {
		question := &request.Questions[index]
		question.ID, question.Prompt = strings.TrimSpace(question.ID), strings.TrimSpace(question.Prompt)
		if question.ID == "" || question.Prompt == "" || seen[question.ID] {
			return request, errors.New("each human input question requires a unique id and prompt")
		}
		seen[question.ID] = true
		if len(question.Options) < 2 || len(question.Options) > 3 || !question.AllowFreeText {
			return request, fmt.Errorf("question %q requires two or three choices plus a free-text alternative", question.ID)
		}
		optionIDs, recommended := map[string]bool{}, 0
		for optionIndex := range question.Options {
			option := &question.Options[optionIndex]
			option.ID, option.Label = strings.TrimSpace(option.ID), strings.TrimSpace(option.Label)
			if option.ID == "" || option.Label == "" || optionIDs[option.ID] {
				return request, fmt.Errorf("question %q has an invalid option", question.ID)
			}
			optionIDs[option.ID] = true
			if option.Recommended {
				recommended++
			}
		}
		if recommended != 1 {
			return request, fmt.Errorf("question %q must identify exactly one recommended choice", question.ID)
		}
		question.Required = true
	}
	return request, nil
}

func (s *Server) listInputRequests(w http.ResponseWriter, r *http.Request) {
	values, err := s.store.ListInputRequests(r.Context(), model.InputRequestFilter{
		RunID: r.URL.Query().Get("run_id"), Status: r.URL.Query().Get("status"), Limit: queryInt(r, "limit", 100),
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	runs := map[string]model.Run{}
	for _, value := range values {
		if run, runErr := s.store.GetRun(r.Context(), value.RunID); runErr == nil {
			runs[value.RunID] = run
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_requests": values, "runs": runs})
}

func (s *Server) inputRequestRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/input-requests/"), "/"), "/")
	if len(parts) == 1 && r.Method == http.MethodGet {
		value, err := s.store.GetInputRequest(r.Context(), parts[0])
		if err != nil {
			writeStoreError(w, err)
			return
		}
		responses, _ := s.store.ListInputResponses(r.Context(), value.ID)
		deliveries, _ := s.store.ListInputDeliveries(r.Context(), value.ID)
		writeJSON(w, http.StatusOK, map[string]any{"input_request": value, "responses": responses, "deliveries": deliveries})
		return
	}
	if len(parts) != 2 || parts[1] != "responses" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	var input struct {
		Answers []model.InputAnswer `json:"answers"`
	}
	if err := decodeJSON(w, r, 64<<10, &input); err != nil {
		return
	}
	request, err := s.store.GetInputRequest(r.Context(), parts[0])
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := validateInputAnswers(request, input.Answers); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	principal := principalFrom(r.Context())
	response := model.InputResponse{Channel: "control_plane", ActorID: principal.Member.ID,
		ActorName: principal.Member.DisplayName, Answers: input.Answers}
	request, response, err = s.store.ResolveInputRequest(r.Context(), request.ID, response)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.recordInputAnswered(r.Context(), request, response)
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "input_request": request, "response": response})
}

func validateInputAnswers(request model.InputRequest, answers []model.InputAnswer) error {
	byQuestion := map[string]model.InputAnswer{}
	known := map[string]bool{}
	for _, question := range request.Questions {
		known[question.ID] = true
	}
	for _, answer := range answers {
		if !known[answer.QuestionID] {
			return fmt.Errorf("answer references unknown question %q", answer.QuestionID)
		}
		if _, duplicate := byQuestion[answer.QuestionID]; duplicate {
			return fmt.Errorf("question %q was answered more than once", answer.QuestionID)
		}
		byQuestion[answer.QuestionID] = answer
	}
	for _, question := range request.Questions {
		answer, ok := byQuestion[question.ID]
		if !ok || (strings.TrimSpace(answer.OptionID) == "" && strings.TrimSpace(answer.Text) == "") {
			return fmt.Errorf("an answer is required for question %q", question.ID)
		}
		if answer.OptionID != "" {
			valid := false
			for _, option := range question.Options {
				valid = valid || option.ID == answer.OptionID
			}
			if !valid {
				return fmt.Errorf("question %q contains an unknown option", question.ID)
			}
		}
	}
	return nil
}

func (s *Server) recordInputAnswered(ctx context.Context, request model.InputRequest, response model.InputResponse) {
	payload, _ := json.Marshal(map[string]any{"request_id": request.ID, "channel": response.Channel,
		"actor_id": response.ActorID, "answers": response.Answers})
	_, _ = s.appendEvent(ctx, model.Event{RunID: request.RunID, Stage: request.Stage, Type: "human_input.answered",
		Level: "info", Message: "Human input received; run queued to resume", Payload: payload})
	_, _ = s.appendEvent(ctx, model.Event{RunID: request.RunID, Stage: request.Stage, Type: "run.resumed",
		Level: "info", Message: "Run queued after human input"})
	if err := s.syncLinearInputAnswered(ctx, request, response); err != nil {
		s.logger.Error("synchronize answered human input to Linear", "request_id", request.ID, "error", err)
	}
	s.broker.Notify()
}

func (s *Server) syncLinearInputRequested(ctx context.Context, request model.InputRequest) error {
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return err
	}
	repository, err := s.store.GetRepository(ctx, run.RepositoryID)
	if err != nil {
		return err
	}
	token, err := s.linearAccessToken(ctx)
	if err != nil {
		_ = s.store.PutInputDelivery(ctx, model.InputDelivery{RequestID: request.ID, Provider: "linear", State: "failed", Error: err.Error()})
		return err
	}
	client := s.linear(token)
	lifecycle, err := s.ensureLinearLifecycleStates(ctx, client, repository.LinearTeamID)
	if err != nil {
		return err
	}
	marker := "<!-- agent-harness:input:" + request.ID + " -->"
	lines := []string{marker, "", "## Input needed by " + request.Stage, "", request.Summary, ""}
	for index, question := range request.Questions {
		lines = append(lines, fmt.Sprintf("### %d. %s", index+1, question.Prompt))
		if question.Why != "" {
			lines = append(lines, question.Why)
		}
		for _, option := range question.Options {
			label := option.Label
			if option.Recommended {
				label += " (recommended)"
			}
			lines = append(lines, "- **"+label+"** — "+option.Description)
		}
		lines = append(lines, "- Or reply with another answer.", "")
	}
	lines = append(lines, "Reply to this comment with answers for all questions. The run is checkpointed and will resume automatically.")
	comment, err := client.UpsertComment(ctx, run.SourceIssueID, marker, strings.Join(lines, "\n"))
	if err != nil {
		_ = s.store.PutInputDelivery(ctx, model.InputDelivery{RequestID: request.ID, Provider: "linear", State: "failed", Error: err.Error()})
		return err
	}
	if err := s.store.PutInputDelivery(ctx, model.InputDelivery{RequestID: request.ID, Provider: "linear",
		State: "delivered", ExternalID: comment.ID, ExternalURL: run.SourceIssueURL}); err != nil {
		return err
	}
	return s.setLinearIssueState(ctx, client, run.ID, "parent-state", run.SourceIssueID, lifecycle.NeedsInput, true)
}

func (s *Server) syncLinearInputAnswered(ctx context.Context, request model.InputRequest, response model.InputResponse) error {
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return err
	}
	repository, err := s.store.GetRepository(ctx, run.RepositoryID)
	if err != nil {
		return err
	}
	token, err := s.linearAccessToken(ctx)
	if err != nil {
		return err
	}
	client := s.linear(token)
	lifecycle, err := s.ensureLinearLifecycleStates(ctx, client, repository.LinearTeamID)
	if err != nil {
		return err
	}
	var answerErr error
	if !strings.EqualFold(response.Channel, "linear") {
		answerErr = s.syncLinearInputAnswerComment(ctx, client, run, request, response)
	}
	stateErr := s.setLinearIssueState(ctx, client, run.ID, "parent-state", run.SourceIssueID, lifecycle.InProgress, true)
	return errors.Join(answerErr, stateErr)
}

func (s *Server) syncLinearInputAnswerComment(ctx context.Context, client *linearapi.Client, run model.Run, request model.InputRequest, response model.InputResponse) error {
	marker := "<!-- agent-harness:input-answer:" + request.ID + " -->"
	body := renderLinearInputAnswer(marker, request, response)
	return s.upsertLinearActivity(ctx, client, run, "input-answer:"+request.ID, marker, body)
}

func renderLinearInputAnswer(marker string, request model.InputRequest, response model.InputResponse) string {
	channel := strings.TrimSpace(response.Channel)
	if strings.EqualFold(channel, "control_plane") {
		channel = "web UI"
	}
	if channel == "" {
		channel = "external channel"
	}
	lines := []string{marker, "", "## Input answered via " + channel}
	if actor := strings.TrimSpace(response.ActorName); actor != "" {
		lines = append(lines, "", "Answered by **"+actor+"**.")
	}
	answers := map[string]model.InputAnswer{}
	knownQuestions := map[string]bool{}
	for _, answer := range response.Answers {
		answers[answer.QuestionID] = answer
	}
	for _, question := range request.Questions {
		knownQuestions[question.ID] = true
		answer, ok := answers[question.ID]
		if !ok {
			continue
		}
		lines = append(lines, "", "### "+question.Prompt)
		if answer.OptionID != "" {
			label := answer.OptionID
			for _, option := range question.Options {
				if option.ID == answer.OptionID {
					label = option.Label
					break
				}
			}
			lines = append(lines, "- Selected: **"+label+"**")
		}
		if text := strings.TrimSpace(answer.Text); text != "" {
			lines = append(lines, "- Answer: "+text)
		}
	}
	for _, answer := range response.Answers {
		if knownQuestions[answer.QuestionID] {
			continue
		}
		lines = append(lines, "", "### Response")
		if option := strings.TrimSpace(answer.OptionID); option != "" {
			lines = append(lines, "- Selected: **"+option+"**")
		}
		if text := strings.TrimSpace(answer.Text); text != "" {
			lines = append(lines, "- Answer: "+text)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *Server) linearInputComment(w http.ResponseWriter, r *http.Request, parsed linear.ParsedWebhook) {
	if parsed.ParentCommentID == "" && parsed.CommentID != "" {
		token, err := s.linearAccessToken(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err)
			return
		}
		lookupCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		comment, err := s.linear(token).Comment(lookupCtx, parsed.CommentID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, fmt.Errorf("resolve Linear comment thread: %w", err))
			return
		}
		if comment.Parent != nil {
			parsed.ParentCommentID = comment.Parent.ID
		}
		if comment.User != nil {
			parsed.ActorID, parsed.ActorName = comment.User.ID, comment.User.Name
		}
	}
	if parsed.Delivery.Action != "create" || parsed.ParentCommentID == "" || parsed.CommentBody == "" ||
		parsed.ActorID == "" || (parsed.ActorType != "user" && parsed.ActorType != "") {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true, "reason": "not_human_question_reply"})
		return
	}
	request, err := s.store.FindInputRequestByDelivery(r.Context(), "linear", parsed.ParentCommentID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ignored": true, "reason": "not_harness_input_thread"})
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	response := model.InputResponse{Channel: "linear", ActorID: parsed.ActorID, ActorName: parsed.ActorName,
		ExternalID: parsed.CommentID, Answers: []model.InputAnswer{{QuestionID: "linear_reply", Text: parsed.CommentBody}}}
	request, response, err = s.store.ResolveInputRequest(r.Context(), request.ID, response)
	if errors.Is(err, store.ErrConflict) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true, "request_id": request.ID})
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.recordInputAnswered(r.Context(), request, response)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "request_id": request.ID, "run_id": request.RunID})
}
