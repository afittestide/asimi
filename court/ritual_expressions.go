package court

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cucumberexpressions "github.com/cucumber/cucumber-expressions/go/v19"

	"github.com/afittestide/asimi/court/tools"
	"github.com/afittestide/asimi/internal/config"
	"github.com/afittestide/asimi/internal/repo"
	"github.com/afittestide/asimi/internal/runners"
	"github.com/afittestide/asimi/internal/utils"
	"github.com/afittestide/asimi/storage"
)

// StepDefKind distinguishes bash commands from builtin handlers
type StepDefKind int

const (
	StepDefBash    StepDefKind = iota // "!" prefix — inline bash command
	StepDefBuiltin                    // matched via cucumber-expressions registry
)

// StepDefEntry is a resolved step definition ready for execution
type StepDefEntry struct {
	Kind    StepDefKind
	Key     string // output key for given context (e.g. "edict", "manifests", or sanitized command)
	Command string // for bash: the raw command; for builtin: the handler name
}

// StepDef maps a cucumber expression pattern to a handler
type StepDef struct {
	Pattern    string // cucumber expression pattern
	HandlerKey string // key used to dispatch to runGiven/runThen
	OutputKey  string // key stored in exec.Data
	expression cucumberexpressions.Expression
}

// StepDefRegistry holds registered step definitions matched via cucumber-expressions
type StepDefRegistry struct {
	paramRegistry *cucumberexpressions.ParameterTypeRegistry
	defs          []StepDef
}

// NewStepDefRegistry creates a registry with built-in given step definitions
func NewStepDefRegistry() *StepDefRegistry {
	r := &StepDefRegistry{
		paramRegistry: cucumberexpressions.NewParameterTypeRegistry(),
	}
	// Register built-in given steps
	builtins := []struct {
		pattern    string
		handlerKey string
		outputKey  string
	}{
		{"the edict details", "get_edict", "edict"},
		{"the court status", "get_court_status", "court_status"},
		{"the manifests", "get_manifests", "manifests"},
		{"the verdicts", "get_verdicts", "verdicts"},
		{"the precedents", "get_precedents", "precedents"},
		{"the earth status", "get_earth_status", "earth_status"},
		{"the borderlands", "get_borderlands", "borderlands"},
		{"manifests for the borderlands", "create_borderland_manifests", "borderland_manifests"},
		{"the edict is sealed", "seal_edict", "sealed"},
		{"the ruler approves", "ask_ruler", "approved"},
		{"a clear working directory", "check_clean_working_directory", "working_directory_clean"},
		{"the infrastructure templates", "get_infrastructure_templates", "infrastructure_templates"},
		{"build the sandbox", "build_sandbox", "sandbox_build"},
		{"the sandbox is ready", "verify_sandbox_ready", "sandbox_ready"},
		{"the sandbox is healthy", "verify_sandbox_up", "sandbox_healthy"},
		{"the project metadata", "get_project_metadata", "project_metadata"},
		{"the edict awaits ruler's seal", "await_ruler_seal", "awaiting_seal"},
		{"the infrastructure is staged", "stage_infrastructure", "infrastructure_staged"},
		{"record the judge's seal", "record_judge_seal", ""},
		{"record the chancellor's seal", "record_chancellor_seal", ""},
		{"record the ling completed", "record_ling_completed", ""},
		{"the verdicts are passed", "check_verdicts_passed", ""},
		{"the precedent is approved", "check_precedent_approved", ""},
		{"the unsealed edicts", "get_unsealed_edicts", "unsealed_edicts"},
		{"the edict lings", "get_lings", "lings"},
		{"the lings form a valid DAG", "check_ling_dag", ""},
		{"a heaven's snapshot", "get_heaven_snapshot", "heaven_snapshot"},
		{"Asimi's versions", "check_asimi_version", "asimi_version"},
	}
	for _, b := range builtins {
		_ = r.Register(b.pattern, b.handlerKey, b.outputKey) // builtin patterns are known-good
	}
	return r
}

// Register adds a step definition to the registry
func (r *StepDefRegistry) Register(pattern, handlerKey, outputKey string) error {
	expr, err := cucumberexpressions.NewCucumberExpression(pattern, r.paramRegistry)
	if err != nil {
		return fmt.Errorf("invalid cucumber expression %q: %w", pattern, err)
	}
	r.defs = append(r.defs, StepDef{
		Pattern:    pattern,
		HandlerKey: handlerKey,
		OutputKey:  outputKey,
		expression: expr,
	})
	return nil
}

// Match finds the first step definition that matches the given text
func (r *StepDefRegistry) Match(text string) (*StepDef, error) {
	for i := range r.defs {
		args, err := r.defs[i].expression.Match(text)
		if err != nil {
			return nil, err
		}
		if args != nil {
			return &r.defs[i], nil
		}
	}
	return nil, nil
}

// runGiven runs a builtin given function and returns the result
func (r *RitualRunner) runGiven(ctx context.Context, exec *RitualExecution, fn string) (interface{}, error) {
	givenKey := exec.EdictKey()
	switch fn {
	case "get_edict":
		if exec.EdictID == 0 {
			r.logger.Warn("failed to get edict", "key", givenKey)
			return map[string]string{"status": "no edict (system event)"}, nil
		}
		return r.getEdict(givenKey)
	case "get_court_status":
		return r.getCourtStatus(givenKey)
	case "get_manifests":
		return r.arrangeGetManifests(givenKey)
	case "get_verdicts":
		return r.arrangeGetVerdicts(givenKey)
	case "get_precedents":
		return r.arrangeGetPrecedents(givenKey)
	case "get_earth_status":
		return r.getEarthStatus(ctx)
	case "get_borderlands":
		return r.getBorderlands(ctx)
	case "create_borderland_manifests":
		return r.createBorderlandManifests(ctx, exec)
	case "check_clean_working_directory":
		return r.checkCleanWorkingDirectory(ctx)
	case "get_infrastructure_templates":
		return r.getInfrastructureTemplates(ctx)
	case "build_sandbox":
		return r.buildSandbox(ctx)
	case "verify_sandbox_ready":
		return r.verifySandboxReady(ctx)
	case "get_project_metadata":
		return r.getProjectMetadata(ctx)
	case "get_unsealed_edicts":
		return r.getUnsealedEdicts(ctx, exec)
	case "get_lings":
		return r.getLings(givenKey)
	case "get_heaven_snapshot":
		return r.getHeavenSnapshot(ctx)
	case "check_asimi_version":
		return r.checkAsimiVersion(ctx)
	default:
		return nil, fmt.Errorf("unknown given function: %s", fn)
	}
}

func (r *RitualRunner) getUnsealedEdicts(ctx context.Context, exec *RitualExecution) (interface{}, error) {
	sealService := storage.NewSealService(r.db)
	edicts, err := sealService.ListActiveEdicts(exec.Username, exec.Project)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, len(edicts))
	for i, e := range edicts {
		result[i] = map[string]interface{}{
			"edict_id":       e.ID,
			"summary":        e.IssueRef,
			"status":         "active",
			"updated_at":     e.UpdatedAt,
			"has_judge":      e.HasJudgeSeal,
			"has_chancellor": e.HasChancellorSeal,
		}
	}
	return result, nil
}

func (r *RitualRunner) getLings(key storage.EdictKey) (interface{}, error) {
	var lings []storage.Ling
	if err := r.db.Where("edict_id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).
		Order("created_at ASC").
		Find(&lings).Error; err != nil {
		return nil, fmt.Errorf("failed to get lings: %w", err)
	}
	if len(lings) == 0 {
		return nil, fmt.Errorf("no lings found for edict %d", key.ID)
	}
	// Return as JSON-friendly maps for template expansion and fork iteration
	result := make([]map[string]interface{}, len(lings))
	for i, l := range lings {
		result[i] = map[string]interface{}{
			"ling_id":      l.LingID,
			"edict_id":     l.EdictID,
			"description":  l.Description,
			"dependencies": l.Dependencies,
			"status":       string(l.Status),
		}
	}
	return result, nil
}

// checkLingDAG ensures ling dependencies form a DAG (no cycles) using DFS.
func checkLingDAG(lingList []storage.Ling) error {
	deps := make(map[string][]string)
	for _, ling := range lingList {
		deps[ling.LingID] = ling.Dependencies
	}

	visited := make(map[string]bool)
	inStack := make(map[string]bool)

	var hasCycle func(id string) bool
	hasCycle = func(id string) bool {
		visited[id] = true
		inStack[id] = true

		for _, dep := range deps[id] {
			if !visited[dep] {
				if hasCycle(dep) {
					return true
				}
			} else if inStack[dep] {
				return true
			}
		}

		inStack[id] = false
		return false
	}

	for _, ling := range lingList {
		if !visited[ling.LingID] {
			if hasCycle(ling.LingID) {
				return fmt.Errorf("circular dependency detected involving ling %s", ling.LingID)
			}
		}
	}
	return nil
}

func (r *RitualRunner) getHeavenSnapshot(ctx context.Context) (interface{}, error) {
	repoInfo := r.repoInfo

	// Get the main branch name (default to "main")
	branch := repoInfo.BranchSlugOrDefault()

	// Get latest commit info from upstream branch
	output, err := runners.HostRun(ctx, runners.Input{
		Command:        fmt.Sprintf("git log -1 --format='%%H' origin/%s", branch),
		Description:    "get latest commit on upstream branch",
		BypassApproval: true,
	}, r.repoInfo.ProjectRoot)
	latestCommit := ""
	if err == nil {
		latestCommit = strings.TrimSpace(output.Output)
		if len(latestCommit) > 7 {
			latestCommit = latestCommit[:7]
		}
	}

	// Calculate age of the commit
	ageStr := ""
	output2, err := runners.HostRun(ctx, runners.Input{
		Command:        fmt.Sprintf("git log -1 --format='%%cr' origin/%s", branch),
		Description:    "get age of upstream commit",
		BypassApproval: true,
	}, r.repoInfo.ProjectRoot)
	if err == nil {
		ageStr = strings.TrimSpace(output2.Output)
	}

	return map[string]string{
		"branch":        branch,
		"latest_commit": latestCommit,
		"age":           ageStr,
	}, nil
}

func (r *RitualRunner) checkAsimiVersion(ctx context.Context) (interface{}, error) {
	latest, hasUpdate, err := utils.CheckForUpdates(ctx)
	if err != nil {
		r.logger.Debug("asimi version check failed", "error", err)
		return map[string]interface{}{
			"current_version": utils.AsimiVersion,
			"has_update":      false,
			"error":           err.Error(),
		}, nil
	}

	result := map[string]interface{}{
		"current_version": utils.AsimiVersion,
		"latest_version":  latest.Version,
		"has_update":      hasUpdate,
		"url":             latest.URL,
	}

	if hasUpdate {
		r.logger.Info("asimi update available",
			"current", utils.AsimiVersion,
			"latest", latest.Version,
			"url", latest.URL,
		)
	} else {
		r.logger.Debug("running latest asimi version", "version", utils.AsimiVersion)
	}

	return result, nil
}

func (r *RitualRunner) getEdict(key storage.EdictKey) (interface{}, error) {
	var edict storage.Edict
	if err := r.db.First(&edict, "id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).Error; err != nil {
		r.logger.Warn("Ritual runner failed to get edict", "key", key)
		return nil, err
	}
	sealService := storage.NewSealService(r.db)
	status, err := sealService.GetEdictStatus(key)
	if err != nil {
		status = storage.EdictActive // default if error
	}
	return map[string]interface{}{
		"edict_id": edict.ID,
		"intent":   edict.Intent,
		"status":   string(status),
	}, nil
}

func (r *RitualRunner) getCourtStatus(key storage.EdictKey) (interface{}, error) {
	// Refuse to run without a project — the shared SQLite database stores
	// edicts from every project under the same user, so an empty project
	// would return everyone's edicts.
	if key.Project == "" {
		return nil, fmt.Errorf("get_court_status: project is required")
	}
	// Use a single SQL query to fetch and filter edicts by derived status.
	// Scope every table — edicts and the seals/zhengming subqueries — to
	// (username, project), since edict IDs are not globally unique across
	// projects sharing the same SQLite database.
	var result []map[string]interface{}
	query := `
SELECT
    e.id, e.session_id, e.issue_ref, e.intent, e.created_at, e.updated_at,
    CASE
        WHEN EXISTS (SELECT 1 FROM seals s WHERE s.edict_id = e.id AND s.username = e.username AND s.project = e.project AND s.minister_id = 'ruler' AND s.stale_at IS NULL) THEN 'sealed'
        WHEN EXISTS (SELECT 1 FROM zhengming_requests z WHERE z.edict_id = e.id AND z.username = e.username AND z.project = e.project AND z.status = 'pending') THEN 'blocked'
        WHEN EXISTS (SELECT 1 FROM seals s WHERE s.edict_id = e.id AND s.username = e.username AND s.project = e.project AND s.minister_id = 'chancellor' AND s.stale_at IS NULL) THEN 'active'
        WHEN EXISTS (SELECT 1 FROM seals s WHERE s.edict_id = e.id AND s.username = e.username AND s.project = e.project AND s.minister_id = 'judge' AND s.stale_at IS NULL) THEN 'active'
        ELSE 'active'
    END as status
FROM edicts e
WHERE e.username = ? AND e.project = ?
  AND NOT EXISTS (SELECT 1 FROM seals s WHERE s.edict_id = e.id AND s.username = e.username AND s.project = e.project AND s.minister_id = 'ruler' AND s.stale_at IS NULL)
ORDER BY e.updated_at DESC
`
	if err := r.db.Raw(query, key.Username, key.Project).Scan(&result).Error; err != nil {
		return nil, err
	}
	return result, nil
}

func (r *RitualRunner) arrangeGetManifests(key storage.EdictKey) (interface{}, error) {
	var manifests []storage.ForgeManifest
	if err := r.db.Where("edict_id = ? AND username = ? AND project = ?", key.ID, key.Username, key.Project).Find(&manifests).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, len(manifests))
	for i, m := range manifests {
		result[i] = map[string]interface{}{
			"manifest_id": m.ManifestID,
			"edict_id":    m.EdictID,
			"ling_id":     m.LingID,
			"file_path":   m.FilePath,
			"func_name":   m.FuncName,
			"content_sha": m.ContentSHA,
			"commit_hash": m.CommitHash,
			"status":      string(m.Status),
			"verdict_id":  m.VerdictID,
			"created_at":  m.CreatedAt,
			"updated_at":  m.UpdatedAt,
		}
	}
	return result, nil
}

func (r *RitualRunner) arrangeGetVerdicts(key storage.EdictKey) (interface{}, error) {
	var verdicts []storage.JudgeVerdict
	err := r.db.Joins("JOIN forge_manifests ON forge_manifests.manifest_id = judge_verdicts.manifest_id").
		Where("forge_manifests.edict_id = ? AND forge_manifests.username = ? AND forge_manifests.project = ?", key.ID, key.Username, key.Project).
		Find(&verdicts).Error
	if err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, len(verdicts))
	for i, v := range verdicts {
		result[i] = map[string]interface{}{
			"verdict_id":  v.VerdictID,
			"manifest_id": v.ManifestID,
			"outcome":     string(v.Outcome),
		}
	}
	return result, nil
}

func (r *RitualRunner) arrangeGetPrecedents(key storage.EdictKey) (interface{}, error) {
	var precedents []storage.CensorPrecedent
	err := r.db.Joins("JOIN forge_manifests ON forge_manifests.manifest_id = censor_precedents.manifest_id").
		Where("forge_manifests.edict_id = ? AND forge_manifests.username = ? AND forge_manifests.project = ?", key.ID, key.Username, key.Project).
		Find(&precedents).Error
	if err != nil {
		return nil, err
	}
	result := make([]interface{}, len(precedents))
	for i, p := range precedents {
		result[i] = map[string]interface{}{
			"precedent_id":  p.PrecedentID,
			"manifest_id":   p.ManifestID,
			"ruling":        string(p.Ruling),
			"principle":     p.Principle,
			"justification": p.Justification,
		}
	}
	return result, nil
}

// arrangeGetSeals queries the edict's seals and returns them as a slice of maps.
// Non-stale seals only — stale seals (invalidated by intent change) are excluded.
func (r *RitualRunner) arrangeGetSeals(key storage.EdictKey) (interface{}, error) {
	var seals []storage.Seal
	if err := r.db.Where("edict_id = ? AND username = ? AND project = ? AND stale_at IS NULL",
		key.ID, key.Username, key.Project).
		Order("sealed_at ASC").
		Find(&seals).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, len(seals))
	for i, s := range seals {
		result[i] = map[string]interface{}{
			"seal_id":     s.SealID,
			"minister_id": s.MinisterID,
			"sealed_at":   s.SealedAt,
		}
	}
	return result, nil
}

// arrangeGetLings queries the edict's lings (sub-tasks) and returns them as a
// slice of maps. Unlike getLings (which errors on empty), this returns an empty
// slice for use in the scratchpad where no lings is a valid state.
func (r *RitualRunner) arrangeGetLings(key storage.EdictKey) (interface{}, error) {
	var lings []storage.Ling
	if err := r.db.Where("edict_id = ? AND username = ? AND project = ?",
		key.ID, key.Username, key.Project).
		Order("created_at ASC").
		Find(&lings).Error; err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, len(lings))
	for i, l := range lings {
		result[i] = map[string]interface{}{
			"ling_id":      l.LingID,
			"description":  l.Description,
			"dependencies": l.Dependencies,
			"status":       string(l.Status),
		}
	}
	return result, nil
}

// getEarthStatus captures the three parts of the Earth realm:
// the capital (git log), the middle kingdom (git diff --staged), and the borderlands (git diff).
func (r *RitualRunner) getEarthStatus(ctx context.Context) (map[string]string, error) {
	result := map[string]string{
		"earth_capital":        "",
		"earth_middle_kingdom": "",
		"earth_borderlands":    "",
	}

	// Git operations always run on host (not in sandbox)
	gitRun := func(cmd, desc string) string {
		output, err := r.runner.Run(ctx, runners.Input{
			Command:     cmd,
			Description: desc,
		})
		if err == nil {
			return output.Output
		}
		return ""
	}

	result["earth_capital"] = gitRun("git log --oneline -20", "get capital status (git log)")
	result["earth_middle_kingdom"] = gitRun("git diff --staged", "get middle kingdom (git diff --staged)")
	result["earth_borderlands"] = gitRun("git diff", "get earth status: borderlands (git diff)")

	return result, nil
}

// getBorderlands captures unstaged changes and untracked files.
func (r *RitualRunner) getBorderlands(ctx context.Context) (interface{}, error) {
	result := map[string]string{
		"borderlands:changes":   "",
		"borderlands:untracked": "",
	}
	// Git operations always run on host
	diff, err := runners.HostRun(ctx, runners.Input{
		Command:        "git diff",
		Description:    "get borderlands (git diff)",
		BypassApproval: true,
	}, r.repoInfo.ProjectRoot)
	if err == nil {
		result["borderlands:changes"] = diff.Output
	}
	untracked, err := runners.HostRun(ctx, runners.Input{
		Command:        "git ls-files --others --exclude-standard",
		Description:    "get borderlands (untracked files)",
		BypassApproval: true,
	}, r.repoInfo.ProjectRoot)
	if err == nil {
		result["borderlands:untracked"] = untracked.Output
	}
	return result, nil
}

// createBorderlandManifests creates forge manifests from unstaged changes.
// It runs git diff --name-only to get changed files, then creates a ForgeManifest
// for each one so the judge can verdict against them through the standard pipeline.
func (r *RitualRunner) createBorderlandManifests(ctx context.Context, exec *RitualExecution) (interface{}, error) {
	key := exec.EdictKey()

	output, err := r.runner.Run(ctx, runners.Input{
		Command:        "git diff --name-only",
		Description:    "list borderland changed files",
		BypassApproval: true,
	})
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only: %w", err)
	}

	files := strings.Split(strings.TrimSpace(output.Output), "\n")
	var manifests []map[string]interface{}
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		manifestID := GenerateID("manifest", fmt.Sprintf("%d", key.ID), "borderland", f)
		manifest := storage.ForgeManifest{
			ManifestID: manifestID,
			EdictID:    key.ID,
			Username:   key.Username,
			Project:    key.Project,
			FilePath:   f,
			Status:     storage.ManifestForged,
		}
		if err := r.db.FirstOrCreate(&manifest).Error; err != nil {
			return nil, fmt.Errorf("failed to create borderland manifest for %s: %w", f, err)
		}
		manifests = append(manifests, map[string]interface{}{
			"manifest_id": manifestID,
			"edict_id":    key.ID,
			"file_path":   f,
			"status":      string(storage.ManifestForged),
		})
	}

	if len(manifests) == 0 {
		return "no borderland changes found", nil
	}
	return manifests, nil
}

// checkCleanWorkingDirectory verifies the working directory is clean (no unstaged changes)
func (r *RitualRunner) checkCleanWorkingDirectory(ctx context.Context) (interface{}, error) {
	repoInfo := r.repoInfo
	if !repoInfo.IsClean() {
		return nil, fmt.Errorf("working directory is not clean: %v", repoInfo)
	}
	return map[string]string{"status": "clean"}, nil
}

// getInfrastructureTemplates creates infrastructure files from embedded templates and returns their paths
func (r *RitualRunner) getInfrastructureTemplates(ctx context.Context) (interface{}, error) {
	// Ensure directory structure exists — use os.MkdirAll with ProjectRoot so
	// paths resolve consistently regardless of process CWD.
	r.logger.Debug("making dirs", "project root", r.repoInfo.ProjectRoot)
	sandboxDir := filepath.Join(r.repoInfo.ProjectRoot, ".agents", "sandbox")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create .agents/sandbox directory: %w", err)
	}

	// Pre-fill project-specific values so the LLM doesn't need to touch these
	// lines. LLM edits that duplicate keys / headers are a common failure mode
	// (toml rejects duplicates, just rejects malformed recipes), so we do the
	// boilerplate substitution deterministically here.
	//
	// Slug precedence: injected projectSlug from config (set explicitly by the user
	// via the :init prompt or the project's asimi.conf) → git remote slug →
	// "local" as a last-ditch fallback. The config value is the source of truth
	// because the user may have set it before any git remote was configured.
	slug := r.projectSlug
	if slug == "" {
		slug = r.repoInfo.Slug
	}
	if slug == "" {
		slug = "local"
	}
	justfile := strings.Replace(dotagentsJustfile, `PROJECT_NAME := "CHANGE_ME"`,
		fmt.Sprintf(`PROJECT_NAME := "%s"`, slug), 1)
	asimiConf := strings.Replace(config.DefaultConfContent(), `image_name = ""`,
		fmt.Sprintf(`image_name = "localhost/asimi/sandbox/%s:latest"`, slug), 1)
	asimiConf = strings.Replace(asimiConf, `project = ""`,
		fmt.Sprintf(`project = "%s"`, slug), 1)

	// Write embedded templates to project root.
	// All paths are prefixed with ProjectRoot via filepath.Join so os.Stat and
	// os.WriteFile resolve against the project root, not the process CWD.
	files := map[string]string{
		filepath.Join(r.repoInfo.ProjectRoot, "Justfile"):                         justfile,
		filepath.Join(r.repoInfo.ProjectRoot, ".agents/asimi.conf"):               asimiConf,
		filepath.Join(r.repoInfo.ProjectRoot, ".agents/sandbox/Dockerfile"):       dotagentsDockerfile,
		filepath.Join(r.repoInfo.ProjectRoot, ".agents/sandbox/bashrc"):           dotagentsBashrc,
		filepath.Join(r.repoInfo.ProjectRoot, ".agents/sandbox/asimi_runtime.sh"): dotagentsAsimiRuntime,
	}

	// Only seed files that don't already exist — preserve any prior customization
	// (LLM edits from a previous step, manual user edits, etc.). Retries receive
	// the Forge's prior output and the failure error in the work prompt, giving the
	// fresh session enough context to correct its approach without resetting files.
	createdFiles := []string{}
	for destPath, content := range files {
		if _, err := os.Stat(destPath); err == nil {
			continue
		}
		if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", destPath, err)
		}
		createdFiles = append(createdFiles, destPath)
	}

	// Sort createdFiles for deterministic output (map iteration order is random).
	sort.Strings(createdFiles)

	return map[string]interface{}{
		"template_files": createdFiles,
		"directories":    []string{filepath.Join(r.repoInfo.ProjectRoot, ".agents"), filepath.Join(r.repoInfo.ProjectRoot, ".agents", "sandbox")},
	}, nil
}

// buildSandbox builds the sandbox container image via `just build-sandbox`.
// Infrastructure files are baselined by getInfrastructureTemplates, which the
// establish-infrastructure step resolves (via its background) before each
// attempt — including on retry — so this function does not need to reset them.
func (r *RitualRunner) buildSandbox(ctx context.Context) (interface{}, error) {
	if r.isolatedHost {
		return map[string]string{"status": "skipped", "output": "isolated-host mode: sandbox not needed"}, nil
	}
	output, err := runners.HostRun(ctx, runners.Input{
		Command:        "just build-sandbox",
		Description:    "build the sandbox",
		BypassApproval: true,
	}, r.repoInfo.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to build the sandbox image: %w", err)
	}
	if output.ExitCode != "0" {
		r.logger.Warn("just build-sandbox failed", "exit_code", output.ExitCode, "output", output.Output)
		return nil, fmt.Errorf("just build-sandbox exited with code %s:\n%s", output.ExitCode, output.Output)
	}

	// Stop and remove the old container so verifySandboxReady creates a fresh
	// one from the rebuilt image.
	if r.runner != nil {
		if closeErr := r.runner.Close(ctx); closeErr != nil {
			r.logger.Warn("failed to close old sandbox container after rebuild", "error", closeErr)
		}
	}

	return map[string]string{"status": "built", "output": output.Output}, nil
}

// verifySandboxUp runs a quick smoke test and verify it's on linux
// It distinguishes between configuration failures (blocking) and transient failures (non-blocking).
func (r *RitualRunner) verifySandboxUp(ctx context.Context) (interface{}, error) {
	if r.isolatedHost {
		return map[string]string{"status": "ready", "output": "isolated-host mode: sandbox check skipped"}, nil
	}
	output, err := r.runner.Run(ctx, runners.Input{
		Command:        "echo $container",
		Description:    "print podman if in a sandbox",
		BypassApproval: true,
	})
	if err != nil {
		return map[string]string{
			"status": "failed",
			"output": "sandbox is broken. To fix :init\n" + output.Output,
		}, fmt.Errorf("sandbox smoke test failed: %w", err)
	}
	if !strings.Contains(output.Output, "podman") {
		return map[string]string{
			"status": "failed",
			"output": "container env var has no podman: " + output.Output,
		}, fmt.Errorf("sandbox smoke test failed. Use `:init` to fix")
	}
	return map[string]string{
		"status": "ready",
		"output": output.Output,
	}, nil
}

func (r *RitualRunner) verifySandboxReady(ctx context.Context) (interface{}, error) {
	if r.isolatedHost {
		return map[string]string{"status": "ready", "output": "isolated-host mode: sandbox check skipped"}, nil
	}
	// Step 1: Reload the runner to pick up the newly built sandbox image
	// Use the injected sandbox config instead of reloading from disk.
	if r.sandboxConfig == nil {
		return map[string]string{
			"status": "failed",
			"output": "sandbox configuration not available",
		}, fmt.Errorf("sandbox configuration not available (SetConfig not called)")
	}
	repoInfo := r.repoInfo
	r.runner = runners.InitShellRunner(r.sandboxConfig, repoInfo)
	if r.runner == nil {
		return map[string]string{
			"status": "failed",
			"output": "container runner not available",
		}, fmt.Errorf("container runner not available")
	}
	if r.runner.RunnerType() != "podman" {
		return map[string]string{
			"status": "failed",
			"output": "failed to bring the container up",
		}, fmt.Errorf("container runner type is %s, expected podman", r.runner.RunnerType())
	}

	// Propagate runner upgrade to Court and all ministers
	if r.onRunnerUpgrade != nil {
		r.onRunnerUpgrade(r.runner)
	}

	// Step 2: Smoke-test the newly built sandbox container
	output, err := r.runner.Run(ctx, runners.Input{
		Command:        "uname",
		Description:    "smoke test the sandbox",
		BypassApproval: true,
	})
	if err == nil && output.ExitCode != "0" {
		r.logger.Warn("sandbox smoke test failed", "exit_code", output.ExitCode, "output", output.Output)
		err = fmt.Errorf("sandbox smoke test exited with code %s: %s", output.ExitCode, output.Output)
	}
	if err != nil {
		return map[string]string{
			"status": "failed",
			"output": "sandbox smoke test failed. Start RCA with .agents/sandbox/Dockerfile and build output:\n" + output.Output,
		}, fmt.Errorf("sandbox smoke test failed: %w", err)
	}
	if !strings.Contains(output.Output, "Linux") {
		return map[string]string{
			"status": "failed",
			"output": "uname output has no Linux: " + output.Output,
		}, fmt.Errorf("sandbox smoke test failed. Use `:init` to fix")
	}

	// Step 3: Install dependencies inside the sandbox (BLOCKING - configuration failure)
	installOutput, installErr := r.runner.Run(ctx, runners.Input{
		Command:        "just install",
		Description:    "install dependencies in sandbox",
		BypassApproval: true,
	})
	if installErr == nil && installOutput.ExitCode != "0" {
		r.logger.Error("just install failed",
			"exit_code", installOutput.ExitCode,
			"output", installOutput.Output)
		return map[string]string{
			"status": "failed",
			"output": "dependency installation failed. Start RCA with dependency versions and install output:\n" + installOutput.Output,
		}, fmt.Errorf("just install failed with exit code %s: %s", installOutput.ExitCode, installOutput.Output)
	}
	if installErr != nil {
		r.logger.Error("just install failed", "error", installErr)
		return map[string]string{
			"status": "failed",
			"output": "dependency installation failed: " + installErr.Error(),
		}, fmt.Errorf("just install failed: %w", installErr)
	}

	// Step 4: Build inside the sandbox to verify it works (BLOCKING)
	output, err = r.runner.Run(ctx, runners.Input{
		Command:        "just build",
		Description:    "verify sandbox by building inside container",
		BypassApproval: true,
	})
	if err == nil && output.ExitCode != "0" {
		r.logger.Warn("just build failed", "exit_code", output.ExitCode, "output", output.Output)
		err = fmt.Errorf("just build exited with code %s: %s", output.ExitCode, output.Output)
	}
	if err != nil {
		return map[string]string{
			"status": "failed",
			"output": "sandbox build failed. Start RCA with build output:\n" + output.Output,
		}, fmt.Errorf("sandbox build failed: %w", err)
	}

	// Step 5: Run tests as non-blocking smoke test (TRANSIENT - may fail due to incomplete test data)
	testOutput, testErr := r.runner.Run(ctx, runners.Input{
		Command:        "just test",
		Description:    "smoke test: verify tests run in sandbox",
		BypassApproval: true,
	})
	if testErr == nil && testOutput.ExitCode != "0" {
		r.logger.Warn("just test failed (non-blocking)",
			"exit_code", testOutput.ExitCode,
			"output", testOutput.Output)
		// Don't return error - tests are optional during sandbox verification
		// They may fail due to missing test data or incomplete setup, not sandbox issues
	}

	return map[string]string{
		"status": "ready",
		"output": output.Output,
	}, nil
}

// getProjectMetadata captures repository information for use in ritual templates
func (r *RitualRunner) getProjectMetadata(ctx context.Context) (interface{}, error) {
	repoInfo := r.repoInfo

	// Use ProjectRoot for remote URL lookup
	root := repoInfo.ProjectRoot

	// Parse host, org, project from remote URL
	host, org, project := "local", "local", "unknown"
	if remote, err := repo.GitRemoteOriginURL(root); err == nil && remote != "" {
		host, org, project = parseHostOrgProject(remote)
	}

	// Derive slug: prefer repoInfo.Slug, fall back to org-project from remote
	slug := repoInfo.Slug
	if slug == "" && org != "local" && project != "unknown" {
		slug = org + "-" + project
	}

	// Extract project name from slug
	projectName := slug
	if idx := strings.LastIndex(slug, "-"); idx >= 0 {
		projectName = slug[idx+1:]
	}
	if projectName == "" {
		projectName = "unknown"
	}

	return map[string]string{
		"project_slug": slug,
		"project_name": projectName,
		"branch":       repoInfo.Branch,
		"host":         host,
		"org":          org,
		"project":      project,
	}, nil
}

// parseHostOrgProject extracts host, organization, and project from a git remote URL
func parseHostOrgProject(remote string) (host, org, project string) {
	host = "github.com" // default
	if strings.Contains(remote, "://") {
		if u, err := url.Parse(remote); err == nil {
			host = u.Host
		}
	} else if strings.Contains(remote, "@") {
		// SSH format: git@github.com:owner/repo.git
		parts := strings.SplitN(remote, "@", 2)
		if len(parts) == 2 {
			hostPart := strings.SplitN(parts[1], ":", 2)
			if len(hostPart) >= 1 {
				host = hostPart[0]
			}
		}
	}

	owner, repoName := repo.ParseGitRemote(remote)
	if owner == "" || repoName == "" {
		return host, "unknown", "unknown"
	}

	return host, repo.SanitizeSegment(owner), repo.SanitizeSegment(repoName)
}

// runThen runs a builtin then function (extensible via step registry)
func (r *RitualRunner) runThen(ctx context.Context, exec *RitualExecution, fn string) error {
	// Non-edict operations run regardless of EdictID
	switch fn {
	case "build_sandbox":
		_, err := r.buildSandbox(ctx)
		return err
	case "verify_sandbox_up":
		_, err := r.verifySandboxUp(ctx)
		return err
	case "verify_sandbox_ready":
		_, err := r.verifySandboxReady(ctx)
		return err
	case "stage_infrastructure":
		// Stage infrastructure files on the host (git runs on host, not in sandbox).
		// Only stage files that actually exist — AGENTS.md may not be present if
		// the LLM chose a different conventions filename, and the core ritual
		// should still succeed.
		paths := []string{"Justfile", ".agents/"}
		for _, p := range []string{"AGENTS.md", "CLAUDE.md"} {
			if _, err := os.Stat(filepath.Join(r.repoInfo.ProjectRoot, p)); err == nil {
				paths = append(paths, p)
			}
		}
		output, err := runners.HostRun(ctx, runners.Input{
			Command:        "git add " + strings.Join(paths, " "),
			Description:    "stage infrastructure files",
			BypassApproval: true,
		}, r.repoInfo.ProjectRoot)
		if err != nil {
			return fmt.Errorf("failed to stage infrastructure: %w", err)
		}
		if output.ExitCode != "0" {
			return fmt.Errorf("git add failed (exit %s): %s", output.ExitCode, output.Output)
		}
		return nil
	}

	// Edict-specific operations require an active edict
	if exec.EdictID == 0 {
		r.logger.Debug("skipping edict operation for system ritual", "fn", fn)
		return nil
	}
	thenKey := exec.EdictKey()
	switch fn {
	case "seal_edict":
		// Sealing is now done via the seal chain - grant ruler seal
		sealService := storage.NewSealService(r.db)
		return sealService.GrantSeal(thenKey, "ruler", storage.JSON{"ritual": exec.RitualName})
	case "ask_ruler":
		// Use the chancellor for zhengming requests, as it's the minister that interacts with the ruler
		// and has a corresponding tab for displaying zhengming questions
		minister := r.getMinister("secretary")
		if minister == nil {
			return fmt.Errorf("minister not found: secretary")
		}
		type zhengmingGate interface {
			RequestZhengming(ctx context.Context, key storage.EdictKey, questions storage.ZhengmingQuestions, priority storage.ZhengmingPriority, callerMinisterID string) (string, error)
		}
		gate, ok := minister.(zhengmingGate)
		if !ok {
			return fmt.Errorf("minister secretary does not support zhengming")
		}
		// Get the step that just completed to include in the question
		stepName := ""
		if exec.CurrentStep >= 0 && exec.CurrentStep < len(exec.def.Steps) {
			stepName = exec.def.Steps[exec.CurrentStep].Name
		}
		questions := storage.ZhengmingQuestions{{
			Text:    fmt.Sprintf("The %s has completed work on edict %d. Do you approve?", stepName, exec.EdictID),
			Options: []string{tools.AnswerApproveAndProceed, tools.AnswerLetMeClarify, tools.AnswerReject},
		}}
		requestID, err := gate.RequestZhengming(ctx, thenKey, questions, storage.PriorityUrgent, "secretary")
		if err != nil {
			return fmt.Errorf("failed to request zhengming: %w", err)
		}
		// Store request_id in execution data (UI notification via zhengming_requested event)
		if exec.Data == nil {
			exec.Data = storage.JSON{}
		}
		exec.Data["pending_zhengming"] = requestID
		// Return sentinel so the caller can block until the ruler answers
		return ErrZhengmingPending
	case "the changes are staged":
		// Stage all changes in the working directory (Borderlands → Middle Kingdom)
		if r.runner == nil {
			return fmt.Errorf("no runner configured for staging changes")
		}
		output, err := r.runner.Run(ctx, runners.Input{
			Command:        "git add -A",
			Description:    "stage all changes (Borderlands → Middle Kingdom)",
			BypassApproval: true,
		})
		if err != nil {
			return fmt.Errorf("failed to stage changes: %w", err)
		}
		if output.ExitCode != "0" {
			return fmt.Errorf("git add failed (exit %s): %s", output.ExitCode, output.Output)
		}
		return nil
	case "await_ruler_seal":
		// Stage only files from manifests (not git add -A)
		var manifests []storage.ForgeManifest
		if err := r.db.Where("edict_id = ? AND username = ? AND project = ?", thenKey.ID, thenKey.Username, thenKey.Project).Find(&manifests).Error; err != nil {
			return fmt.Errorf("failed to query manifests: %w", err)
		}

		if len(manifests) > 0 {
			// Extract file paths, splitting comma-separated paths
			var files []string
			for _, m := range manifests {
				for _, f := range strings.Split(m.FilePath, ",") {
					f = strings.TrimSpace(f)
					if f != "" {
						files = append(files, f)
					}
				}
			}

			// Stage specific files
			cmd := "git add " + strings.Join(files, " ")
			output, err := r.runner.Run(ctx, runners.Input{
				Command:        cmd,
				Description:    "stage manifest files",
				BypassApproval: true,
			})
			if err != nil {
				return fmt.Errorf("failed to stage manifests: %w", err)
			}
			if output.ExitCode != "0" {
				return fmt.Errorf("git add failed (exit %s): %s", output.ExitCode, output.Output)
			}
		}

		// TODO: Raise event - awaiting ruler's seat
		return nil
	case "record_judge_seal":
		// Record the judge's seal on the edict
		sealService := storage.NewSealService(r.db)
		if err := sealService.GrantSeal(thenKey, "judge", storage.JSON{"ritual": exec.RitualName}); err != nil {
			return fmt.Errorf("failed to record judge's seal: %w", err)
		}
		return nil
	case "record_chancellor_seal":
		// Record the chancellor's seal on the edict
		sealService := storage.NewSealService(r.db)
		if err := sealService.GrantSeal(thenKey, "chancellor", storage.JSON{"ritual": exec.RitualName}); err != nil {
			return fmt.Errorf("failed to record chancellor's seal: %w", err)
		}
		return nil
	case "record_ling_completed":
		// Mark the current fork item's ling as completed
		if item, ok := exec.Data["item"].(map[string]interface{}); ok {
			lingID, ok := item["ling_id"].(string)
			if !ok || lingID == "" {
				return nil // no ling_id in item, skip silently
			}
			result := r.db.Model(&storage.Ling{}).
				Where("ling_id = ? AND username = ? AND project = ?", lingID, thenKey.Username, thenKey.Project).
				Update("status", storage.LingDone)
			if result.Error != nil {
				return fmt.Errorf("failed to mark ling completed: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("ling not found: %s", lingID)
			}
		}
		return nil
	case "check_verdicts_passed":
		// Check all manifests for this edict - fail if any of the latest per file_path are rejected.
		// Only the most recent manifest per file_path matters; superseded rejected manifests
		// should not trigger false failures after reforging.
		var manifests []storage.ForgeManifest
		if err := r.db.Raw(`
			SELECT * FROM forge_manifests fm
			WHERE fm.edict_id = ? AND fm.username = ? AND fm.project = ?
			  AND fm.created_at = (
			    SELECT MAX(fm2.created_at) FROM forge_manifests fm2
			    WHERE fm2.edict_id = fm.edict_id
			      AND fm2.username = fm.username
			      AND fm2.project = fm.project
			      AND fm2.file_path = fm.file_path
			  )`,
			thenKey.ID, thenKey.Username, thenKey.Project).
			Scan(&manifests).Error; err != nil {
			return fmt.Errorf("failed to query manifests: %w", err)
		}
		var rejected []string
		for _, m := range manifests {
			if m.Status == storage.ManifestRejected {
				rejected = append(rejected, m.FilePath)
			}
		}
		if len(rejected) > 0 {
			return fmt.Errorf("verdict check failed: %d manifest(s) rejected: %v", len(rejected), rejected)
		}

		// Check JudgeVerdict outcomes using latest-wins per manifest:
		// For each manifest, only the most recent verdict (by created_at) matters.
		// Fail only if the latest verdict for any manifest is failed.
		var failedVerdicts []storage.JudgeVerdict
		if err := r.db.Raw(`
			SELECT jv.* FROM judge_verdicts jv
			JOIN forge_manifests fm ON fm.manifest_id = jv.manifest_id
			WHERE fm.edict_id = ? AND fm.username = ? AND fm.project = ?
			  AND jv.created_at = (
			    SELECT MAX(jv2.created_at) FROM judge_verdicts jv2
			    WHERE jv2.manifest_id = jv.manifest_id
			  )
			  AND jv.outcome = ?`,
			thenKey.ID, thenKey.Username, thenKey.Project, storage.VerdictFailed).
			Scan(&failedVerdicts).Error; err != nil {
			return fmt.Errorf("failed to query verdicts: %w", err)
		}
		if len(failedVerdicts) > 0 {
			// Collect manifest IDs for error reporting
			manifestIDs := make([]string, len(failedVerdicts))
			for i, v := range failedVerdicts {
				manifestIDs[i] = v.ManifestID
			}
			return fmt.Errorf("verdict check failed: %d verdict(s) with failed outcome: %v", len(failedVerdicts), manifestIDs)
		}

		// Also check edict-level verdicts (manifest_id = '')
		var failedEdictVerdicts []storage.JudgeVerdict
		if err := r.db.Raw(`
			SELECT * FROM judge_verdicts jv
			WHERE jv.manifest_id = '' AND jv.test_suite = 'edict'
			  AND jv.username = ? AND jv.project = ?
			  AND jv.created_at = (
			    SELECT MAX(jv2.created_at) FROM judge_verdicts jv2
			    WHERE jv2.manifest_id = '' AND jv2.test_suite = 'edict'
			      AND jv2.username = jv.username AND jv2.project = jv.project
			  )
			  AND jv.outcome = ?`,
			thenKey.Username, thenKey.Project, storage.VerdictFailed).
			Scan(&failedEdictVerdicts).Error; err != nil {
			return fmt.Errorf("failed to query edict-level verdicts: %w", err)
		}
		if len(failedEdictVerdicts) > 0 {
			return fmt.Errorf("verdict check failed: edict-level verdict %s has failed outcome", failedEdictVerdicts[0].VerdictID)
		}
		return nil
	case "check_precedent_approved":
		// Check censor precedents using latest-wins per manifest:
		// For each manifest, only the most recent precedent (by created_at) matters.
		// Fail only if the latest precedent for any manifest is rejected.
		var rejectedPrecedents []storage.CensorPrecedent
		if err := r.db.Raw(`
			SELECT cp.* FROM censor_precedents cp
			JOIN forge_manifests fm ON fm.manifest_id = cp.manifest_id
			WHERE fm.edict_id = ? AND fm.username = ? AND fm.project = ?
			  AND cp.created_at = (
			    SELECT MAX(cp2.created_at) FROM censor_precedents cp2
			    WHERE cp2.manifest_id = cp.manifest_id
			  )
			  AND cp.ruling = ?`,
			thenKey.ID, thenKey.Username, thenKey.Project, storage.PrecedentRejected).
			Scan(&rejectedPrecedents).Error; err != nil {
			return fmt.Errorf("failed to query precedents: %w", err)
		}
		if len(rejectedPrecedents) > 0 {
			// Build detailed error with principle + justification for each rejection
			var details []string
			for _, p := range rejectedPrecedents {
				detail := fmt.Sprintf("principle=%q", p.Principle)
				if p.Justification != "" {
					detail += fmt.Sprintf(" justification=%q", p.Justification)
				}
				details = append(details, detail)
			}
			return fmt.Errorf("precedent check failed: %d precedent(s) rejected — %s", len(rejectedPrecedents), strings.Join(details, "; "))
		}

		// Also check edict-level precedents (manifest_id = ''), scoped to the
		// CURRENT edict via edict_id. An edict-level precedent only has force
		// over the edict it belongs to. If the current edict owns no edict-level
		// record at all, the check is satisfied — a rejection for another edict
		// must NOT block this one. Block only if the LATEST edict-level record
		// for THIS edict is rejected.
		var rejectedEdictPrecedents []storage.CensorPrecedent
		if err := r.db.Raw(`
			SELECT * FROM censor_precedents cp
			WHERE cp.manifest_id = ''
			  AND cp.edict_id = ?
			  AND cp.created_at = (
			    SELECT MAX(cp2.created_at) FROM censor_precedents cp2
			    WHERE cp2.manifest_id = '' AND cp2.edict_id = cp.edict_id
			  )
			  AND cp.ruling = ?`,
			thenKey.ID, storage.PrecedentRejected).
			Scan(&rejectedEdictPrecedents).Error; err != nil {
			return fmt.Errorf("failed to query edict-level precedents: %w", err)
		}
		if len(rejectedEdictPrecedents) > 0 {
			p := rejectedEdictPrecedents[0]
			detail := fmt.Sprintf("principle=%q", p.Principle)
			if p.Justification != "" {
				detail += fmt.Sprintf(" justification=%q", p.Justification)
			}
			return fmt.Errorf("precedent check failed: edict-level precedent %s is rejected — %s", p.PrecedentID, detail)
		}
		return nil
	case "check_asimi_version":
		// Warn if not running the latest Asimi version - non-blocking check
		// This is handled as a "then" step that logs a warning but doesn't fail
		return nil
	case "check_ling_dag":
		// Validate that the edict's lings form a valid DAG (no cycles)
		var lings []storage.Ling
		if err := r.db.Where("edict_id = ? AND username = ? AND project = ?", thenKey.ID, thenKey.Username, thenKey.Project).
			Find(&lings).Error; err != nil {
			return fmt.Errorf("failed to query lings for DAG check: %w", err)
		}
		if len(lings) == 0 {
			return nil // no lings, nothing to validate
		}
		return checkLingDAG(lings)
	default:
		return fmt.Errorf("unknown then function: %s", fn)
	}
}

// resolveStepDef resolves a raw step string into a StepDefEntry.
// "!" prefix → bash command, else matched via cucumber-expressions registry.
func (r *RitualRunner) resolveStepDef(raw string) (StepDefEntry, error) {
	if strings.HasPrefix(raw, "!") {
		cmd := strings.TrimPrefix(raw, "!")
		// Sanitize command into a key: take first word
		key := strings.Fields(cmd)[0]
		key = strings.ReplaceAll(key, "/", "_")

		// Check for ` # variable_name` suffix to override the storage key.
		// Only treat # as a comment when preceded by whitespace (bash convention)
		// so that # inside arguments (e.g., sed 's/#//') is not mistaken.
		if idx := strings.Index(cmd, " #"); idx >= 0 {
			comment := strings.TrimSpace(cmd[idx+2:])
			if parts := strings.Fields(comment); len(parts) > 0 {
				key = parts[0]
			}
			cmd = strings.TrimSpace(cmd[:idx])
		}

		return StepDefEntry{
			Kind:    StepDefBash,
			Key:     key,
			Command: cmd,
		}, nil
	}

	def, err := r.stepDefs.Match(raw)
	if err != nil {
		return StepDefEntry{}, fmt.Errorf("step matching error: %w", err)
	}
	if def == nil {
		return StepDefEntry{}, fmt.Errorf("no step definition matches %q", raw)
	}
	return StepDefEntry{
		Kind:    StepDefBuiltin,
		Key:     def.OutputKey,
		Command: def.HandlerKey,
	}, nil
}

// storeGivenResult stores a given step result directly into exec.Data.
// map[string]string results are flattened into the parent object for convenience.
func storeGivenResult(exec *RitualExecution, key string, result interface{}) {
	if exec.Data == nil {
		exec.Data = storage.JSON{}
	}
	if m, ok := result.(map[string]string); ok {
		for k, v := range m {
			exec.Data[k] = v
		}
	} else {
		exec.Data[key] = result
	}
}

// runGivenStep executes a single given step and returns its result
// Ported from PR #135 (Nanook contribution): cmd_running/cmd_done
// notifications for shell steps, complementary to ToolCallScheduled/Success.

func (r *RitualRunner) runGivenStep(ctx context.Context, exec *RitualExecution, entry StepDefEntry) (interface{}, error) {
	switch entry.Kind {
	case StepDefBash:
		if r.runner == nil {
			return nil, fmt.Errorf("no runner configured for bash given step")
		}
		cmd := r.expandTemplate(entry.Command, exec)
		exec.Notify(RitualStepMsg{
			StepName: entry.Key,
			Status:   "cmd_running",
			Message:  cmd,
		})
		output, err := r.runner.Run(ctx, runners.Input{
			Command:        cmd,
			Description:    fmt.Sprintf("given: %s", entry.Command),
			BypassApproval: true,
		})
		exec.Notify(RitualStepMsg{
			StepName: entry.Key,
			Status:   "cmd_done",
			Message:  output.Output,
		})
		if err != nil {
			return nil, err
		}
		if output.ExitCode != "0" {
			storeGivenResult(exec, entry.Key, output.Output)
			exec.Data["error"] = output.Output
			return nil, fmt.Errorf("given failed (exit %s): %s", output.ExitCode, utils.TruncateMiddle(output.Output, 500))
		}
		return output.Output, nil
	case StepDefBuiltin:
		return r.runGiven(ctx, exec, entry.Command)
	default:
		return nil, fmt.Errorf("unknown step kind: %d", entry.Kind)
	}
}

// runThenStep executes a single then step and returns an error if it fails
func (r *RitualRunner) runThenStep(ctx context.Context, exec *RitualExecution, entry StepDefEntry) error {
	switch entry.Kind {
	case StepDefBash:
		if r.runner == nil {
			return fmt.Errorf("no runner configured for bash then step")
		}
		cmd := r.expandTemplate(entry.Command, exec)
		exec.Notify(RitualStepMsg{
			StepName: entry.Key,
			Status:   "cmd_running",
			Message:  cmd,
		})
		output, err := r.runner.Run(ctx, runners.Input{
			Command:        cmd,
			Description:    fmt.Sprintf("then: %s", entry.Command),
			BypassApproval: true,
		})
		exec.Notify(RitualStepMsg{
			StepName: entry.Key,
			Status:   "cmd_done",
			Message:  output.Output,
		})
		if err != nil {
			return err
		}
		if output.ExitCode != "0" {
			return fmt.Errorf("then failed (exit %s): %s", output.ExitCode, utils.TruncateMiddle(output.Output, 500))
		}
		return nil
	case StepDefBuiltin:
		return r.runThen(ctx, exec, entry.Command)
	default:
		return fmt.Errorf("unknown step kind: %d", entry.Kind)
	}
}
