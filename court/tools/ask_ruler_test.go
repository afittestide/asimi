package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/afittestide/asimi/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// mockRequester captures the EdictKey passed to RequestZhengming
type mockRequester struct {
	capturedKey              storage.EdictKey
	capturedQuestions        storage.ZhengmingQuestions
	capturedCallerMinisterID string
	calls                    int
}

func (m *mockRequester) RequestZhengming(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error) {
	m.capturedKey = key
	m.capturedQuestions = questions
	m.capturedCallerMinisterID = callerMinisterID
	m.calls++
	return "req-123", nil
}

func TestAskRulerTool_KeyIncludesUsernameAndProject(t *testing.T) {
	mock := &mockRequester{}
	tool := AskRulerTool{
		MinisterID: "secretary",
		Requester:  mock,
		Username:   "daonb",
		Project:    "afittestide-asimi-cli",
	}

	input := `{
		"edict_id": 42,
		"questions": [{"text": "Which approach?", "options": ["A", "B"]}]
	}`

	_, err := tool.Call(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.capturedKey.Username != "daonb" {
		t.Errorf("expected username 'daonb', got %q", mock.capturedKey.Username)
	}
	if mock.capturedKey.Project != "afittestide-asimi-cli" {
		t.Errorf("expected project 'afittestide-asimi-cli', got %q", mock.capturedKey.Project)
	}
	if mock.capturedKey.ID != 42 {
		t.Errorf("expected edict_id 42, got %d", mock.capturedKey.ID)
	}
}

func TestAskRulerTool_CallPassesMinisterIDAsCallerMinisterID(t *testing.T) {
	mock := &mockRequester{}
	tool := AskRulerTool{
		MinisterID: "chancellor",
		Requester:  mock,
		Username:   "daonb",
		Project:    "afittestide-asimi-cli",
	}

	input := `{
		"edict_id": 99,
		"questions": [{"text": "Approve?", "options": ["Yes", "No"]}]
	}`

	_, err := tool.Call(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.capturedCallerMinisterID != "chancellor" {
		t.Errorf("expected callerMinisterID 'chancellor', got %q", mock.capturedCallerMinisterID)
	}
}

func TestAskRulerTool_CallPassesMinisterIDAsCallerMinisterID_ForDifferentMinisters(t *testing.T) {
	tests := []struct {
		ministerID string
	}{
		{"secretary"},
		{"chancellor"},
		{"war"},
		{"judge"},
		{"forge"},
	}

	for _, tt := range tests {
		t.Run(tt.ministerID, func(t *testing.T) {
			mock := &mockRequester{}
			tool := AskRulerTool{
				MinisterID: tt.ministerID,
				Requester:  mock,
				Username:   "daonb",
				Project:    "afittestide-asimi-cli",
			}

			input := `{
				"edict_id": 1,
				"questions": [{"text": "OK?", "options": ["Yes", "No"]}]
			}`

			_, err := tool.Call(context.Background(), input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if mock.capturedCallerMinisterID != tt.ministerID {
				t.Errorf("expected callerMinisterID %q, got %q", tt.ministerID, mock.capturedCallerMinisterID)
			}
		})
	}
}

func setupSuggestEdictTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := db.AutoMigrate(&storage.Edict{}, &storage.Seal{}, &storage.Zhengming{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return db
}

func TestNewEdictTool_PassesZeroEdictIDInKey(t *testing.T) {
	db := setupSuggestEdictTestDB(t)
	mock := &mockRequester{}
	tool := NewEdictTool{
		Ctx: ToolContext{
			DB:         db,
			Username:   "sageuser",
			Project:    "myproject",
			MinisterID: "secretary",
		},
		Requester: mock,
	}

	input := `{
		"suggestion": "Create a new edict for something",
		"summary": "new edict",
		"issue_ref": "afittestide/asimi-cli#168"
	}`

	_, err := tool.Call(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.capturedKey.ID != 0 {
		t.Errorf("expected edict_id 0 for new edict, got %d", mock.capturedKey.ID)
	}
	if mock.capturedCallerMinisterID != "secretary" {
		t.Errorf("expected callerMinisterID 'secretary', got %q", mock.capturedCallerMinisterID)
	}
	// The issue_ref rides the question text as an Issue-Ref annotation.
	if len(mock.capturedQuestions) != 1 || mock.capturedQuestions[0].Text == "" {
		t.Fatalf("expected one question with text")
	}
	if !strings.Contains(mock.capturedQuestions[0].Text, "Issue-Ref: afittestide/asimi-cli#168") {
		t.Errorf("expected Issue-Ref annotation in question text, got:\n%s", mock.capturedQuestions[0].Text)
	}
}

func TestReplaceEdictTool_PassesEdictIDInKey(t *testing.T) {
	db := setupSuggestEdictTestDB(t)

	// Create an existing edict
	edict := storage.Edict{
		ID:       10,
		Username: "sageuser",
		Project:  "myproject",
		Intent:   "Original intent",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	mock := &mockRequester{}
	tool := ReplaceEdictTool{
		Ctx: ToolContext{
			DB:         db,
			Username:   "sageuser",
			Project:    "myproject",
			MinisterID: "chancellor",
		},
		Requester: mock,
	}

	input := `{
		"suggestion": "Add more tests",
		"summary": "improve test coverage",
		"edict_id": 10
	}`

	_, err := tool.Call(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mock.capturedKey.ID != 10 {
		t.Errorf("expected edict_id 10 in key, got %d", mock.capturedKey.ID)
	}
	if mock.capturedKey.Username != "sageuser" {
		t.Errorf("expected username 'sageuser', got %q", mock.capturedKey.Username)
	}
	if mock.capturedKey.Project != "myproject" {
		t.Errorf("expected project 'myproject', got %q", mock.capturedKey.Project)
	}
	if mock.capturedCallerMinisterID != "chancellor" {
		t.Errorf("expected callerMinisterID 'chancellor', got %q", mock.capturedCallerMinisterID)
	}
}

func TestReplaceEdictTool_NonexistentEdictReturnsError(t *testing.T) {
	db := setupSuggestEdictTestDB(t)
	mock := &mockRequester{}
	tool := ReplaceEdictTool{
		Ctx: ToolContext{
			DB:         db,
			Username:   "sageuser",
			Project:    "myproject",
			MinisterID: "chancellor",
		},
		Requester: mock,
	}

	input := `{
		"suggestion": "Refine something",
		"summary": "refinement",
		"edict_id": 999
	}`

	if _, err := tool.Call(context.Background(), input); err == nil {
		t.Fatal("expected error for nonexistent edict, got nil")
	}
	if mock.calls != 0 {
		t.Errorf("expected no zhengming raised for a nonexistent target, got %d calls", mock.calls)
	}
}

func TestReplaceEdictTool_WrongUserProjectReturnsError(t *testing.T) {
	db := setupSuggestEdictTestDB(t)

	// Create an edict belonging to a different user/project
	edict := storage.Edict{
		ID:       5,
		Username: "otheruser",
		Project:  "otherproject",
		Intent:   "Someone else's edict",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	mock := &mockRequester{}
	tool := ReplaceEdictTool{
		Ctx: ToolContext{
			DB:         db,
			Username:   "sageuser",
			Project:    "myproject",
			MinisterID: "chancellor",
		},
		Requester: mock,
	}

	input := fmt.Sprintf(`{
		"suggestion": "Refine edict",
		"summary": "refinement",
		"edict_id": %d
	}`, edict.ID)

	_, err := tool.Call(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for edict belonging to different user/project, got nil")
	}
}

// TestReplaceEdictTool_RefusesCancelledTargetWithoutRaisingZhengming is the
// e873 Defect-5 guard: a cancelled edict must be refused at CALL time, before
// any zhengming is raised, so the Ruler's approval can never be consumed by a
// dead target (the observed failure: the gh #180 design approved against
// cancelled edict 180 and lost).
func TestReplaceEdictTool_RefusesCancelledTargetWithoutRaisingZhengming(t *testing.T) {
	db := setupSuggestEdictTestDB(t)
	cancelled := time.Date(2026, 4, 5, 12, 0, 0, 0, time.UTC)
	edict := storage.Edict{
		ID:          180,
		Username:    "sageuser",
		Project:     "myproject",
		Intent:      "A tombstone",
		CancelledAt: &cancelled,
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}

	mock := &mockRequester{}
	tool := ReplaceEdictTool{
		Ctx: ToolContext{
			DB:         db,
			Username:   "sageuser",
			Project:    "myproject",
			MinisterID: "chancellor",
		},
		Requester: mock,
	}

	input := `{
		"suggestion": "The gh 180 mandate design",
		"summary": "trust dialog mandate chooser",
		"edict_id": 180
	}`

	_, err := tool.Call(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for cancelled target, got nil")
	}
	if !strings.Contains(err.Error(), "cancelled") || !strings.Contains(err.Error(), "new_edict") {
		t.Errorf("error should name the state and instruct to use new_edict, got: %v", err)
	}
	if mock.calls != 0 {
		t.Errorf("no zhengming must be raised for a cancelled target, got %d calls", mock.calls)
	}
}

// TestReplaceEdictTool_RefusesSealedTargetWithoutRaisingZhengming: a
// ruler-sealed edict's intent is frozen history; refining it would silently
// invalidate a completed seal chain.
func TestReplaceEdictTool_RefusesSealedTargetWithoutRaisingZhengming(t *testing.T) {
	db := setupSuggestEdictTestDB(t)
	edict := storage.Edict{
		ID:       7,
		Username: "sageuser",
		Project:  "myproject",
		Intent:   "Sealed intent",
	}
	if err := db.Create(&edict).Error; err != nil {
		t.Fatalf("failed to create edict: %v", err)
	}
	if err := storage.NewSealService(db).GrantSeal(storage.EdictKey{ID: 7, Username: "sageuser", Project: "myproject"}, "ruler", nil); err != nil {
		t.Fatalf("failed to grant ruler seal: %v", err)
	}

	mock := &mockRequester{}
	tool := ReplaceEdictTool{
		Ctx: ToolContext{
			DB:         db,
			Username:   "sageuser",
			Project:    "myproject",
			MinisterID: "chancellor",
		},
		Requester: mock,
	}

	input := `{
		"suggestion": "Rewrite the sealed intent",
		"summary": "rewrite",
		"edict_id": 7
	}`

	_, err := tool.Call(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for sealed target, got nil")
	}
	if !strings.Contains(err.Error(), "sealed") || !strings.Contains(err.Error(), "new_edict") {
		t.Errorf("error should name the state and instruct to use new_edict, got: %v", err)
	}
	if mock.calls != 0 {
		t.Errorf("no zhengming must be raised for a sealed target, got %d calls", mock.calls)
	}
}

// TestEdictToolDescriptionsStateRulerModifiedContract: both tools' descriptions
// must state the zhengming (async request) nature, that the tool writes
// nothing itself, and the ruler_modified loop.
func TestEdictToolDescriptionsStateRulerModifiedContract(t *testing.T) {
	for _, tool := range []Tool{NewEdictTool{}, ReplaceEdictTool{}} {
		desc := tool.Description()
		if !strings.Contains(desc, "ruler_modified") {
			t.Errorf("%s description must mention ruler_modified, got:\n%s", tool.Name(), desc)
		}
		if !strings.Contains(desc, "nothing was saved") {
			t.Errorf("%s description must state nothing was saved on ruler_modified, got:\n%s", tool.Name(), desc)
		}
		if !strings.Contains(desc, "Zhengming") {
			t.Errorf("%s description must state the zhengming (async request) nature, got:\n%s", tool.Name(), desc)
		}
		if !strings.Contains(desc, "writes nothing") {
			t.Errorf("%s description must state the tool writes nothing itself, got:\n%s", tool.Name(), desc)
		}
	}
}
