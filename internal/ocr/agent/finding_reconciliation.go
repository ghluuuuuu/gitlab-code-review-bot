package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/llm"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/llmloop"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/session"
)

const reconciliationDiffLimit = 120_000

type reconciliationResponse struct {
	Decisions []session.FindingDecision `json:"decisions"`
}

func findingReferenceID(prefix, path string, index int, comment model.LlmComment) string {
	payload := fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s", prefix, path, index, comment.Content, comment.SuggestionCode, comment.ExistingCode)
	digest := sha256.Sum256([]byte(payload))
	return prefix + "-" + fmt.Sprintf("%x", digest[:8])
}

func (a *Agent) reconcileHistoricalFindings(ctx context.Context) error {
	entry := session.FindingTimeline{
		StartSHA: a.inputResolution.ResolvedBase, EndSHA: a.inputResolution.ResolvedHead,
		ExactRange: a.inputResolution.ExactRange, CommitSHA: a.inputResolution.ResolvedHead,
		Timestamp: time.Now().UTC(),
	}
	if entry.CommitSHA == "" {
		entry.CommitSHA = a.args.To
	}
	if a.args.Resume != nil {
		entry.PreviousSessionID = a.args.Resume.SessionID
	}

	manifest := a.session.FinalManifest()
	completed := make(map[string]struct{})
	if manifest != nil {
		for _, item := range manifest.Coverage.Completed {
			if item.Path != "" {
				completed[item.Path] = struct{}{}
			}
		}
	}
	if len(completed) == 0 {
		return nil
	}
	entry.ChangedFiles = a.reconciliationScopes(completed)
	var historical []session.FindingReference
	if a.args.Resume != nil {
		paths := make([]string, 0, len(a.args.Resume.LatestComments))
		for path := range a.args.Resume.LatestComments {
			if _, ok := completed[path]; ok {
				paths = append(paths, path)
			}
		}
		sort.Strings(paths)
		for _, path := range paths {
			for index, comment := range a.args.Resume.LatestComments[path] {
				historical = append(historical, session.FindingReference{ID: findingReferenceID("historical", path, index, comment), Comment: comment})
			}
		}
	}

	currentByPath := make(map[string][]model.LlmComment)
	for _, comment := range a.currentCommentsForReconciliation(completed) {
		currentByPath[comment.Path] = append(currentByPath[comment.Path], comment)
	}
	paths := make([]string, 0, len(currentByPath))
	for path := range currentByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var current []session.FindingReference
	for _, path := range paths {
		for index, comment := range currentByPath[path] {
			current = append(current, session.FindingReference{ID: findingReferenceID("current", path, index, comment), Comment: comment})
		}
	}
	entry.Historical, entry.Current = historical, current

	if len(historical) == 0 {
		for _, ref := range current {
			entry.Decisions = append(entry.Decisions, session.FindingDecision{CurrentID: ref.ID, Status: session.FindingStatusNew, Confidence: "high", Reason: "本次审查中首次发现"})
		}
		a.session.RecordFindingTimeline(entry)
		return nil
	}

	task := a.args.Template.FindingReconciliationTask
	if task == nil || len(task.Messages) == 0 {
		entry = conservativeReconciliation(entry, "缺少缺陷闭合判定模板，保守保留历史问题")
		a.session.RecordFindingTimeline(entry)
		return nil
	}

	historicalJSON, _ := json.Marshal(historical)
	currentJSON, _ := json.Marshal(current)
	scopeJSON, _ := json.Marshal(entry.ChangedFiles)
	diffs := a.reconciliationDiffs(completed)
	messages := make([]llm.Message, 0, len(task.Messages))
	for _, message := range task.Messages {
		content := strings.NewReplacer(
			"{{commit_sha}}", entry.CommitSHA,
			"{{start_sha}}", entry.StartSHA,
			"{{end_sha}}", entry.EndSHA,
			"{{exact_range}}", entry.ExactRange,
			"{{changed_files}}", string(scopeJSON),
			"{{historical_findings}}", string(historicalJSON),
			"{{current_findings}}", string(currentJSON),
			"{{diffs}}", diffs,
		).Replace(message.Content)
		messages = append(messages, llm.NewTextMessage(message.Role, content))
	}

	record := a.session.GetOrCreateFileSession("__finding_reconciliation__").AppendTaskRecord(session.FindingReconciliationTask, messages)
	started := time.Now()
	resp, err := a.args.LLMClient.CompletionsWithCtx(ctx, llm.ChatRequest{Model: a.args.Model, Messages: messages, MaxTokens: 4000})
	if err != nil {
		record.SetError(err, time.Since(started))
		entry = conservativeReconciliation(entry, "缺陷闭合 AI 调用失败，保守保留历史问题")
		a.session.RecordFindingTimeline(entry)
		return fmt.Errorf("reconcile historical findings: %w", err)
	}
	record.SetResponse(resp, time.Since(started))
	a.runner.RecordUsage(resp.Usage)
	var parsed reconciliationResponse
	body := strings.TrimSpace(llmloop.StripMarkdownFences(resp.Content()))
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		entry = conservativeReconciliation(entry, "缺陷闭合 AI 返回格式无效，保守保留历史问题")
		entry.AdjudicationError = err.Error()
		a.session.RecordFindingTimeline(entry)
		return fmt.Errorf("parse finding reconciliation: %w", err)
	}
	entry.Decisions = validateReconciliation(parsed.Decisions, historical, current)
	a.session.RecordFindingTimeline(entry)
	return nil
}

func (a *Agent) reconciliationScopes(completed map[string]struct{}) []session.ChangedFileScope {
	scopes := make([]session.ChangedFileScope, 0)
	for _, item := range a.diffs {
		path := effectivePath(item)
		if _, ok := completed[path]; !ok {
			continue
		}
		scope := session.ChangedFileScope{Path: path, OldPath: item.OldPath}
		for _, line := range strings.Split(item.Diff, "\n") {
			if strings.HasPrefix(line, "@@") {
				scope.Hunks = append(scope.Hunks, line)
			}
		}
		scopes = append(scopes, scope)
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].Path < scopes[j].Path })
	return scopes
}

func (a *Agent) currentCommentsForReconciliation(completed map[string]struct{}) []model.LlmComment {
	comments := a.runner.CollectPendingComments()
	result := make([]model.LlmComment, 0, len(comments))
	for _, comment := range comments {
		if _, ok := completed[comment.Path]; ok {
			result = append(result, comment)
		}
	}
	return result
}

func (a *Agent) reconciliationDiffs(completed map[string]struct{}) string {
	var builder strings.Builder
	for _, item := range a.diffs {
		path := effectivePath(item)
		if _, ok := completed[path]; !ok {
			continue
		}
		section := fmt.Sprintf("\n### %s\n%s\n", path, item.Diff)
		if builder.Len()+len(section) > reconciliationDiffLimit {
			builder.WriteString("\n[diff input truncated]\n")
			break
		}
		builder.WriteString(section)
	}
	return builder.String()
}

func conservativeReconciliation(entry session.FindingTimeline, reason string) session.FindingTimeline {
	entry.Decisions = make([]session.FindingDecision, 0, len(entry.Historical)+len(entry.Current))
	for _, ref := range entry.Historical {
		entry.Decisions = append(entry.Decisions, session.FindingDecision{HistoricalID: ref.ID, Status: session.FindingStatusUnfixed, Confidence: "low", Reason: reason})
	}
	for _, ref := range entry.Current {
		entry.Decisions = append(entry.Decisions, session.FindingDecision{CurrentID: ref.ID, Status: session.FindingStatusNew, Confidence: "low", Reason: "本次发现未能与历史问题完成可靠关联"})
	}
	entry.AdjudicationError = reason
	return entry
}

func validateReconciliation(decisions []session.FindingDecision, historical, current []session.FindingReference) []session.FindingDecision {
	oldIDs, currentIDs := make(map[string]struct{}, len(historical)), make(map[string]struct{}, len(current))
	for _, ref := range historical {
		oldIDs[ref.ID] = struct{}{}
	}
	for _, ref := range current {
		currentIDs[ref.ID] = struct{}{}
	}
	seenOld, seenCurrent := make(map[string]bool), make(map[string]bool)
	result := make([]session.FindingDecision, 0, len(historical)+len(current))
	for _, decision := range decisions {
		if _, ok := oldIDs[decision.HistoricalID]; !ok && decision.HistoricalID != "" {
			continue
		}
		if _, ok := currentIDs[decision.CurrentID]; !ok && decision.CurrentID != "" {
			continue
		}
		if decision.Status != session.FindingStatusFixed && decision.Status != session.FindingStatusUnfixed && decision.Status != session.FindingStatusNew {
			continue
		}
		if decision.HistoricalID != "" && seenOld[decision.HistoricalID] {
			continue
		}
		if decision.CurrentID != "" && seenCurrent[decision.CurrentID] {
			continue
		}
		if decision.Status == session.FindingStatusFixed && decision.CurrentID != "" {
			continue
		}
		if decision.HistoricalID != "" {
			seenOld[decision.HistoricalID] = true
		}
		if decision.CurrentID != "" {
			seenCurrent[decision.CurrentID] = true
		}
		result = append(result, decision)
	}
	for _, ref := range historical {
		if !seenOld[ref.ID] {
			result = append(result, session.FindingDecision{HistoricalID: ref.ID, Status: session.FindingStatusUnfixed, Confidence: "low", Reason: "AI 未覆盖该历史问题，保守保留"})
		}
	}
	for _, ref := range current {
		if !seenCurrent[ref.ID] {
			result = append(result, session.FindingDecision{CurrentID: ref.ID, Status: session.FindingStatusNew, Confidence: "low", Reason: "AI 未将该问题关联到历史问题"})
		}
	}
	return result
}
