package contract

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/gastownhall/gascity/internal/fsys"
)

// PreflightBDContext is the bd-reported backend state for a beads scope.
type PreflightBDContext struct {
	Backend       string
	DoltMode      string
	BDVersion     string
	SchemaVersion int
}

// PreflightChecker evaluates whether a beads scope may use native storage.
type PreflightChecker struct {
	// FS reads .beads/metadata.json. A nil FS uses fsys.OSFS.
	FS fsys.FS
	// Provider is the already-resolved beads provider name from configuration.
	Provider string
	// BDContext reads bd context state for the scope.
	BDContext func(scope string) (PreflightBDContext, error)
	// DatabaseProjectID reads the authoritative database _project_id for the scope.
	DatabaseProjectID func(scope string) (string, bool, error)
	// DeferIdentityToNativeOpen reports whether, when the direct database probe
	// cannot confirm project_id, the scope should stay native-eligible and defer
	// authoritative identity verification to beadslib's native-open path
	// (verifyProjectIdentity over the authenticated connection) instead of
	// degrading off the native store. It is true for external endpoints such as
	// a hosted beads-gateway, whose EIA-as-username + TLS credential-command auth
	// the control-plane root/plaintext probe cannot replicate, but whose database
	// _project_id beadslib still verifies at open time — refusing to connect, and
	// falling back to BdStore, on mismatch. Nil defaults to no deferral (Warn).
	DeferIdentityToNativeOpen func(scope string) bool
	// BeadsLibraryVersion is the linked github.com/steveyegge/beads module
	// version. Empty means infer it from build info.
	BeadsLibraryVersion string
	// BeadsLibraryReplaced reports whether a go.mod replace directive supplied
	// the linked beads library, in which case its version identifies the
	// replacement rather than a beads release. Set together with
	// BeadsLibraryVersion; leaving both zero infers them from build info.
	BeadsLibraryReplaced bool
	// DatabaseSchemaVersion reads the beads schema migration version already
	// applied to the scope's database. A nil reader cannot prove a native open
	// would leave the schema alone, so the schema_migration check degrades the
	// scope to BdStore rather than assuming it is safe.
	DatabaseSchemaVersion func(scope string) (int, bool, error)
	// LinkedSchemaVersion is the schema version the linked beads library was
	// built against — beadsschema.LatestVersion() at the composition root.
	// Opening the native store applies every migration between the database's
	// applied version and this one, so it is the version a native open would
	// leave behind. Zero means unknown.
	LinkedSchemaVersion int
	// AllowSchemaMigration disarms the schema_migration gate for a caller that
	// deliberately intends to migrate this city's databases. It is the
	// designated-migrator escape hatch, the local twin of beads' own
	// BD_ALLOW_REMOTE_MIGRATE.
	AllowSchemaMigration bool
}

// Check runs the beads backend preflight for scope and returns typed diagnostics.
func (c PreflightChecker) Check(scope string) (PreflightResult, error) {
	metadata, err := c.readMetadata(scope)
	if err != nil {
		return PreflightResult{}, err
	}
	bdCtx, bdCtxErr := c.readBDContext(scope)

	checks := []PreflightCheckResult{
		c.checkProvider(),
		c.checkMetadataBackend(metadata),
		c.checkBDContextAgreement(metadata, bdCtx, bdCtxErr),
		c.checkDoltModeSafe(metadata, bdCtx, bdCtxErr),
		c.checkIdentityMatch(scope, metadata),
		c.checkVersionCompat(bdCtx, bdCtxErr),
		c.checkSchemaMigration(scope),
		c.checkContractShape(metadata),
	}
	verdict := preflightVerdictForChecks(checks)
	// A DEGRADED verdict caused solely by an unreachable bd context (e.g. a
	// non-git city root where `bd context` cannot resolve a repo root) is
	// upgraded to ELIGIBLE when gc has INDEPENDENTLY verified the dolt backend
	// — the identity_match check connects to the dolt server and matches
	// project_id. That direct verification is stronger evidence than bd
	// context's cross-check, so an inability to also cross-verify via bd's
	// cwd-sensitive context command must not force the per-call bd fallback.
	eligibleViaIdentityFallback := false
	if verdict == PreflightVerdictDegraded && bdCtxErr != nil && degradedOnlyByUnreachableBDContext(checks) {
		verdict = PreflightVerdictEligible
		eligibleViaIdentityFallback = true
	}
	result := PreflightResult{
		Verdict:                           verdict,
		Scope:                             scope,
		Checks:                            checks,
		RepairSteps:                       preflightRepairSteps(checks),
		NativeStoreEligible:               verdict == PreflightVerdictEligible,
		NativeEligibleViaIdentityFallback: eligibleViaIdentityFallback,
	}
	if verdict != PreflightVerdictEligible {
		result.Fallback = PreflightFallbackBdStore
		result.FallbackReason = preflightFallbackReason(checks)
	}
	return NewPreflightResult(result), nil
}

func (c PreflightChecker) readMetadata(scope string) (preflightMetadata, error) {
	files := c.FS
	if files == nil {
		files = fsys.OSFS{}
	}
	path := filepath.Join(scope, ".beads", "metadata.json")
	data, err := files.ReadFile(path)
	if err != nil {
		return preflightMetadata{}, fmt.Errorf("read preflight metadata %s: %w", path, err)
	}
	var metadata preflightMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return preflightMetadata{}, fmt.Errorf("parse preflight metadata %s: %w", path, err)
	}
	return metadata.trimmed(), nil
}

func (c PreflightChecker) checkProvider() PreflightCheckResult {
	provider := strings.TrimSpace(c.Provider)
	details := PreflightDetails{Provider: provider}
	switch {
	case ProviderUsesBDContract(provider):
		return NewPreflightCheckResult(PreflightCheckProviderContract, PreflightCheckPass, "Provider exposes bd contract", details)
	case provider == "":
		return NewPreflightCheckResult(PreflightCheckProviderContract, PreflightCheckFail, "Beads provider is not configured", details)
	default:
		return NewPreflightCheckResult(PreflightCheckProviderContract, PreflightCheckFail, fmt.Sprintf("Provider %q does not expose the bd contract", provider), details)
	}
}

// ProviderUsesBDContract reports whether provider exposes the bd-compatible
// store contract needed for native-store preflight and fallback decisions.
func ProviderUsesBDContract(provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" || provider == "bd" {
		return true
	}
	if !strings.HasPrefix(provider, "exec:") {
		return false
	}
	base := strings.TrimSuffix(filepath.Base(strings.TrimPrefix(provider, "exec:")), ".sh")
	return base == "gc-beads-bd"
}

func (c PreflightChecker) checkMetadataBackend(metadata preflightMetadata) PreflightCheckResult {
	details := PreflightDetails{MetadataBackend: metadata.Backend}
	switch metadata.Backend {
	case "dolt":
		return NewPreflightCheckResult(PreflightCheckMetadataBackend, PreflightCheckPass, "Metadata backend is dolt", details)
	case "":
		return NewPreflightCheckResult(PreflightCheckMetadataBackend, PreflightCheckFail, "Metadata backend is missing", details)
	default:
		return NewPreflightCheckResult(PreflightCheckMetadataBackend, PreflightCheckFail, fmt.Sprintf("Metadata backend %q is unsupported; the native store serves dolt only", metadata.Backend), details)
	}
}

func (c PreflightChecker) readBDContext(scope string) (PreflightBDContext, error) {
	if c.BDContext == nil {
		return PreflightBDContext{}, fmt.Errorf("bd context reader is not configured")
	}
	ctx, err := c.BDContext(scope)
	ctx.Backend = strings.TrimSpace(ctx.Backend)
	ctx.DoltMode = strings.TrimSpace(ctx.DoltMode)
	ctx.BDVersion = strings.TrimSpace(ctx.BDVersion)
	return ctx, err
}

func (c PreflightChecker) checkBDContextAgreement(metadata preflightMetadata, ctx PreflightBDContext, err error) PreflightCheckResult {
	details := PreflightDetails{MetadataBackend: metadata.Backend}
	details.BDContextBackend = ctx.Backend
	if err != nil {
		// Unreachable bd context (e.g. a non-git city root where `bd context`
		// cannot run) is not evidence of backend DISAGREEMENT — only that we
		// cannot cross-verify. Degrade (opt-in) rather than hard-block; a real
		// mismatch is still caught below once bd context is readable.
		return NewPreflightCheckResult(PreflightCheckBDContextAgreement, PreflightCheckWarn, "bd context is unreachable; cannot cross-verify backend agreement", details)
	}
	if details.MetadataBackend == "" || details.BDContextBackend == "" {
		return NewPreflightCheckResult(PreflightCheckBDContextAgreement, PreflightCheckFail, "bd context agreement cannot be determined", details)
	}
	if details.MetadataBackend != details.BDContextBackend {
		return NewPreflightCheckResult(PreflightCheckBDContextAgreement, PreflightCheckFail, fmt.Sprintf("Metadata backend=%s; bd context reports backend=%s", details.MetadataBackend, details.BDContextBackend), details)
	}
	return NewPreflightCheckResult(PreflightCheckBDContextAgreement, PreflightCheckPass, "bd context agrees with metadata backend", details)
}

func (c PreflightChecker) checkDoltModeSafe(metadata preflightMetadata, ctx PreflightBDContext, err error) PreflightCheckResult {
	details := PreflightDetails{
		MetadataBackend:   metadata.Backend,
		BDContextBackend:  ctx.Backend,
		BDContextDoltMode: ctx.DoltMode,
	}
	if err != nil {
		// Unreachable bd context cannot confirm dolt server mode; degrade
		// (opt-in) rather than hard-block. embedded mode is still rejected
		// below once bd context is readable.
		return NewPreflightCheckResult(PreflightCheckDoltModeSafe, PreflightCheckWarn, "bd context is unreachable; cannot confirm dolt server mode", details)
	}
	if metadata.Backend != "dolt" || ctx.Backend != "dolt" {
		return NewPreflightCheckResult(PreflightCheckDoltModeSafe, PreflightCheckPass, "Dolt mode check is not required for non-dolt backend", details)
	}
	switch ctx.DoltMode {
	case "server":
		return NewPreflightCheckResult(PreflightCheckDoltModeSafe, PreflightCheckPass, "bd context reports dolt server mode", details)
	case "embedded":
		return NewPreflightCheckResult(PreflightCheckDoltModeSafe, PreflightCheckFail, "dolt_mode=embedded; native store requires Dolt server mode (bd context must report dolt_mode=server) — falling back to per-call bd. See troubleshooting.", details)
	default:
		return NewPreflightCheckResult(PreflightCheckDoltModeSafe, PreflightCheckFail, "bd context reports unsupported dolt mode", details)
	}
}

func (c PreflightChecker) checkIdentityMatch(scope string, metadata preflightMetadata) PreflightCheckResult {
	details := PreflightDetails{MetadataProjectID: metadata.ProjectID}
	if metadata.ProjectID == "" {
		return NewPreflightCheckResult(PreflightCheckIdentityMatch, PreflightCheckFail, "metadata project_id is missing", details)
	}
	if c.DatabaseProjectID == nil {
		return NewPreflightCheckResult(PreflightCheckIdentityMatch, PreflightCheckWarn, "database project_id reader is not configured", details)
	}
	dbProjectID, ok, err := c.DatabaseProjectID(scope)
	details.DBProjectID = strings.TrimSpace(dbProjectID)
	if err != nil || !ok || details.DBProjectID == "" {
		// The direct SQL probe connects as root over plaintext and cannot
		// authenticate an external hosted beads-gateway, whose identity is proven
		// by an EIA-as-username + TLS credential command the control plane does
		// not replicate here. For such endpoints the authoritative database
		// _project_id is verified by beadslib at native-open time
		// (verifyProjectIdentity over the authenticated connection), which
		// refuses to connect on mismatch and drops the scope to BdStore — the
		// same open-time gate BdStore itself relies on. Defer to that gate rather
		// than claiming a confirmation the control plane cannot make, so the
		// scope stays native-eligible without a false proof. A local endpoint,
		// whose probe should have succeeded, still degrades so its genuine probe
		// failure is not silently ignored.
		if c.DeferIdentityToNativeOpen != nil && c.DeferIdentityToNativeOpen(scope) {
			return NewPreflightCheckResult(PreflightCheckIdentityMatch, PreflightCheckPass, "database identity deferred to native-open verification (external endpoint)", details)
		}
		return NewPreflightCheckResult(PreflightCheckIdentityMatch, PreflightCheckWarn, "database project_id could not be confirmed", details)
	}
	if metadata.ProjectID != details.DBProjectID {
		return NewPreflightCheckResult(PreflightCheckIdentityMatch, PreflightCheckFail, "project_id mismatch", details)
	}
	return NewPreflightCheckResult(PreflightCheckIdentityMatch, PreflightCheckPass, "project_id matches", details)
}

func (c PreflightChecker) checkVersionCompat(ctx PreflightBDContext, err error) PreflightCheckResult {
	library := c.linkedBeadsLibrary()
	libraryVersion := strings.TrimPrefix(library.Version, "v")
	details := PreflightDetails{
		BDVersion:           ctx.BDVersion,
		BeadsLibraryVersion: libraryVersion,
		SchemaVersion:       ctx.SchemaVersion,
	}
	if err != nil {
		// Unreachable bd context cannot confirm bd/beads version parity; degrade
		// (opt-in) rather than hard-block. A real version skew is still caught
		// below once bd context is readable.
		return NewPreflightCheckResult(PreflightCheckVersionCompat, PreflightCheckWarn, "bd context is unreachable; cannot confirm bd/beads version compatibility", details)
	}
	if ctx.SchemaVersion <= 0 {
		return NewPreflightCheckResult(PreflightCheckVersionCompat, PreflightCheckFail, "bd context did not report a schema version", details)
	}
	if ctx.BDVersion == "" {
		return NewPreflightCheckResult(PreflightCheckVersionCompat, PreflightCheckWarn, "bd/beads version compatibility could not be confirmed", details)
	}
	if reason := library.unconfirmableReason(); reason != "" {
		// The compare below only means something when both sides name the same
		// released beads artifact. When the linked library does not — a source
		// build, a replaced module, or a pseudo-version naming an untagged
		// commit — the two strings can never be equal, and answering "mismatch"
		// reports a verdict the check never had the evidence to reach. So an
		// unconfirmable library version must not take the native store offline;
		// only a *confirmed* mismatch (below) should.
		//
		// Nothing here speaks to schema compatibility, and ctx.SchemaVersion
		// cannot stand in for it: `bd context --json` reports no database
		// schema version, so that field carries bd's JSON envelope version
		// (the constant JSONSchemaVersion) and the guard above only
		// establishes that the envelope parsed. Reading it as a schema verdict
		// is what let a source-built gc migrate a live city (ga-o6k).
		// checkSchemaMigration is the check that compares database schema
		// state against the linked library.
		return NewPreflightCheckResult(PreflightCheckVersionCompat, PreflightCheckPass, "bd/beads library versions not comparable; schema state is checked separately ("+reason+")", details)
	}
	if strings.TrimPrefix(ctx.BDVersion, "v") != libraryVersion {
		if newerSemverCompatibleBD(ctx.BDVersion, libraryVersion) {
			return NewPreflightCheckResult(PreflightCheckVersionCompat, PreflightCheckPass, "bd version is a newer semver-compatible release of the linked beads library version", details)
		}
		return NewPreflightCheckResult(PreflightCheckVersionCompat, PreflightCheckFail, "bd version differs from linked beads library version", details)
	}
	return NewPreflightCheckResult(PreflightCheckVersionCompat, PreflightCheckPass, "bd and linked beads library versions match", details)
}

// checkSchemaMigration refuses a native open that would migrate the scope's
// database schema, in either direction.
//
// Opening the native store hands the database to the linked beads library,
// which applies every pending migration in-process before serving a single
// query. In a Gas City that is a destructive act with no dry-run margin: one
// managed Dolt server backs every database, and the binaries pointed at it are
// of deliberately different vintage. gc pins beads at the bleeding-edge
// BD_CURRENT_REF (TestBDVersionPins enforces that pin), while agents reach the
// same databases through the installed, released bd CLI. So a gc built from a
// tree whose pin has moved links migrations no installed bd understands, and
// merely connecting migrates the store past the whole town. That is
// gastownhall/gascity ga-o6k: a source-built gc migrated the live city from
// schema v53 to v59 between 00:01:48Z and 00:03:32Z on 2026-09-11, after which
// every beads-backed command town-wide failed with "database is at v59, binary
// knows up to v53".
//
// beads has its own gate for the same hazard (schema.RemoteMigrateGateError),
// but it fires only when the database has a Dolt remote configured — its
// threat model is two clones forking through a shared remote. gc's managed
// databases are local-only, so that gate returns early and never sees this.
// The equivalent gate for a shared local server has to live here.
//
// A FAIL degrades the scope to BdStore, which reaches the database through the
// installed bd binary and therefore cannot migrate past it either. The scope
// keeps working; only the in-process fast path is withheld.
func (c PreflightChecker) checkSchemaMigration(scope string) PreflightCheckResult {
	details := PreflightDetails{LinkedSchemaVersion: c.LinkedSchemaVersion}
	if c.AllowSchemaMigration {
		return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckPass, "schema-migration gate disarmed by the designated migrator", details)
	}
	if c.DatabaseSchemaVersion == nil {
		// Fail-safe: without the probe there is no evidence a native open
		// would leave the schema alone, and the cost of being wrong is the
		// whole city's ledger.
		return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckWarn, "database schema version reader is not configured", details)
	}
	current, ok, err := c.DatabaseSchemaVersion(scope)
	details.DBSchemaVersion = current
	if err != nil || !ok {
		// Mirrors checkIdentityMatch: the direct probe connects as root over
		// plaintext and cannot authenticate an external hosted endpoint. Such
		// a database is not this operator's local city and carries a remote of
		// its own, so beads' own remote-migrate gate covers it; deferring here
		// keeps external endpoints exactly as eligible as they are today.
		if c.DeferIdentityToNativeOpen != nil && c.DeferIdentityToNativeOpen(scope) {
			return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckPass, "schema migration deferred to native-open verification (external endpoint)", details)
		}
		return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckWarn, "database schema version could not be confirmed", details)
	}
	if current == 0 {
		// No schema_migrations cursor: the database has never been migrated,
		// so a native open bootstraps it rather than moving an existing city
		// off a version the installed bd can read. beads' own gate treats
		// version 0 the same way.
		return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckPass, "database has no applied schema; a native open bootstraps it", details)
	}
	if c.LinkedSchemaVersion <= 0 {
		return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckWarn, "linked beads library schema version is unknown", details)
	}
	if c.LinkedSchemaVersion > current {
		return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckFail, fmt.Sprintf(
			"opening the native store would migrate this database from schema v%d to v%d; only a designated migrator may move a city's schema",
			current, c.LinkedSchemaVersion), details)
	}
	if current > c.LinkedSchemaVersion {
		return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckFail, fmt.Sprintf(
			"database schema v%d is ahead of the linked beads library (v%d); this binary cannot serve it",
			current, c.LinkedSchemaVersion), details)
	}
	return NewPreflightCheckResult(PreflightCheckSchemaMigration, PreflightCheckPass, "database schema matches the linked beads library", details)
}

// newerSemverCompatibleBD reports whether bdVersion is a semver-compatible
// upgrade of libraryVersion: the same major version, and not older. Per
// semver, only a major bump breaks compatibility, so a newer bd release
// within the linked library's major version — the common case when an
// unversioned Homebrew dependency drifts ahead of gc's pinned go.mod release
// (gastownhall/gascity#5164) — is safe to treat as compatible rather than a
// hard version mismatch. Returns false (falling back to the exact-match
// behavior already applied above) whenever either version does not parse as
// valid semver, so this only ever widens what passes, never what fails.
func newerSemverCompatibleBD(bdVersion, libraryVersion string) bool {
	bdCanonical := "v" + strings.TrimPrefix(strings.TrimSpace(bdVersion), "v")
	libCanonical := "v" + strings.TrimPrefix(strings.TrimSpace(libraryVersion), "v")
	if !semver.IsValid(bdCanonical) || !semver.IsValid(libCanonical) {
		return false
	}
	if semver.Major(bdCanonical) != semver.Major(libCanonical) {
		return false
	}
	return semver.Compare(bdCanonical, libCanonical) >= 0
}

func (c PreflightChecker) checkContractShape(metadata preflightMetadata) PreflightCheckResult {
	details := PreflightDetails{MetadataBackend: metadata.Backend}
	switch metadata.Backend {
	case "dolt":
		return NewPreflightCheckResult(PreflightCheckContractShape, PreflightCheckPass, "Metadata uses dolt shape", details)
	case "":
		return NewPreflightCheckResult(PreflightCheckContractShape, PreflightCheckFail, "metadata backend is missing", details)
	default:
		return NewPreflightCheckResult(PreflightCheckContractShape, PreflightCheckFail, fmt.Sprintf("metadata backend %q has unsupported contract shape", metadata.Backend), details)
	}
}

func preflightFallbackReason(checks []PreflightCheckResult) string {
	for _, check := range checks {
		if check.State == PreflightCheckFail {
			return check.Summary
		}
	}
	for _, check := range checks {
		if check.State == PreflightCheckWarn {
			return check.Summary
		}
	}
	return ""
}

// beadsModulePath is the module path of the beads library gc links.
const beadsModulePath = "github.com/steveyegge/beads"

// linkedBeadsLibrary resolves the beads library this binary links, preferring
// the configured override so tests need not depend on their own build info.
func (c PreflightChecker) linkedBeadsLibrary() beadsLibrary {
	if version := strings.TrimSpace(c.BeadsLibraryVersion); version != "" || c.BeadsLibraryReplaced {
		return beadsLibrary{Version: version, Replaced: c.BeadsLibraryReplaced}
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return beadsLibrary{}
	}
	return linkedBeadsLibraryFrom(info)
}

// beadsLibrary describes the beads library a binary actually links.
type beadsLibrary struct {
	// Version is the module version recorded in build info, empty when unknown.
	Version string
	// Replaced reports whether a go.mod replace directive supplied the code.
	// A replacement's version numbers belong to the replacement, not to beads,
	// so they say nothing about which bd release the library agrees with.
	Replaced bool
}

// unconfirmableReason names why the library version cannot be compared to a bd
// release version, or returns "" when the comparison is meaningful.
func (b beadsLibrary) unconfirmableReason() string {
	version := strings.TrimPrefix(strings.TrimSpace(b.Version), "v")
	switch {
	case version == "" || version == "(devel)":
		// A source build reports "(devel)" (or nothing) even though gc and bd
		// are built from the same tree.
		return "source build"
	case b.Replaced:
		return "replaced module"
	case !comparableReleaseVersion(version):
		// A pseudo-version names an untagged commit, not a release. gc pins
		// beads at one whenever it tracks beads ahead of its last tag.
		return "pseudo-version"
	default:
		return ""
	}
}

// comparableReleaseVersion reports whether version names a released beads
// artifact, and can therefore be compared with the version bd reports for
// itself. Pseudo-versions and non-semver strings cannot.
func comparableReleaseVersion(version string) bool {
	canonical := "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
	return semver.IsValid(canonical) && !module.IsPseudoVersion(canonical)
}

// linkedBeadsLibraryFrom extracts the linked beads library from build info.
func linkedBeadsLibraryFrom(info *debug.BuildInfo) beadsLibrary {
	if info == nil {
		return beadsLibrary{}
	}
	for _, dep := range info.Deps {
		if dep == nil || dep.Path != beadsModulePath {
			continue
		}
		if dep.Replace != nil {
			// Report the replacement's own version — including the empty one a
			// local-path replace records — never the require line it displaced.
			return beadsLibrary{Version: dep.Replace.Version, Replaced: true}
		}
		return beadsLibrary{Version: dep.Version}
	}
	return beadsLibrary{}
}

type preflightMetadata struct {
	Backend      string `json:"backend"`
	DoltMode     string `json:"dolt_mode"`
	DoltDatabase string `json:"dolt_database"`
	ProjectID    string `json:"project_id"`
}

func (m preflightMetadata) trimmed() preflightMetadata {
	m.Backend = strings.TrimSpace(m.Backend)
	m.DoltMode = strings.TrimSpace(m.DoltMode)
	m.DoltDatabase = strings.TrimSpace(m.DoltDatabase)
	m.ProjectID = strings.TrimSpace(m.ProjectID)
	return m
}

func preflightVerdictForChecks(checks []PreflightCheckResult) PreflightVerdict {
	hasWarn := false
	for _, check := range checks {
		switch check.State {
		case PreflightCheckFail:
			return PreflightVerdictBlocked
		case PreflightCheckWarn:
			hasWarn = true
		}
	}
	if hasWarn {
		return PreflightVerdictDegraded
	}
	return PreflightVerdictEligible
}

// degradedOnlyByUnreachableBDContext reports whether a DEGRADED verdict is safe
// to upgrade to ELIGIBLE. It is true only when the identity_match check PASSED
// (gc independently connected to the dolt server and matched project_id) and
// every non-passing check is a WARN from a bd-context-dependent check — i.e.
// the sole cause of the degrade is that `bd context` could not run. Any FAIL,
// or any WARN from a non-bd-context check, makes it false so the per-call bd
// fallback is preserved.
func degradedOnlyByUnreachableBDContext(checks []PreflightCheckResult) bool {
	identityVerified := false
	for _, check := range checks {
		switch check.State {
		case PreflightCheckFail:
			return false
		case PreflightCheckWarn:
			if !isBDContextDependentCheck(check.ID) {
				return false
			}
		}
		if check.ID == PreflightCheckIdentityMatch && check.State == PreflightCheckPass {
			identityVerified = true
		}
	}
	return identityVerified
}

// isBDContextDependentCheck reports whether a check derives its verdict from
// `bd context` output and therefore WARNs (rather than FAILs) when bd context
// is unreachable.
func isBDContextDependentCheck(id PreflightCheckID) bool {
	switch id {
	case PreflightCheckBDContextAgreement, PreflightCheckDoltModeSafe, PreflightCheckVersionCompat:
		return true
	default:
		return false
	}
}

func preflightRepairSteps(checks []PreflightCheckResult) []PreflightRepairStep {
	var steps []PreflightRepairStep
	for _, check := range checks {
		switch check.ID {
		case PreflightCheckMetadataBackend:
			if check.State == PreflightCheckFail {
				steps = append(steps, PreflightRepairStep{
					CheckID:  check.ID,
					Priority: PreflightRepairRecommended,
					Command:  "bd bootstrap",
					Note:     "Re-anchor metadata to the active beads backend, or continue using BdStore for a scope the native store cannot serve.",
				})
			}
		case PreflightCheckBDContextAgreement:
			if check.State == PreflightCheckFail {
				steps = append(steps, PreflightRepairStep{
					CheckID:  check.ID,
					Priority: PreflightRepairRecommended,
					Command:  "bd context --json",
					Note:     "Inspect which .beads scope bd resolves before repairing metadata.",
				})
			}
		case PreflightCheckDoltModeSafe:
			if check.State == PreflightCheckFail {
				steps = append(steps, PreflightRepairStep{
					CheckID:  check.ID,
					Priority: PreflightRepairRecommended,
					Command:  "bd context --json",
					Note:     "Native store activation requires Dolt server mode.",
				})
			}
		case PreflightCheckIdentityMatch:
			if check.State == PreflightCheckFail {
				steps = append(steps, PreflightRepairStep{
					CheckID:  check.ID,
					Priority: PreflightRepairCritical,
					Command:  "bd doctor --fix",
					Note:     "Identity mismatch is the highest-severity failure.",
				})
			}
		case PreflightCheckVersionCompat:
			if check.State == PreflightCheckFail {
				steps = append(steps, PreflightRepairStep{
					CheckID:  check.ID,
					Priority: PreflightRepairRecommended,
					Command:  "bd doctor",
					Note:     "Verify the installed bd CLI and linked beads library are compatible.",
				})
			}
		case PreflightCheckSchemaMigration:
			if check.State == PreflightCheckFail {
				steps = append(steps, PreflightRepairStep{
					CheckID:  check.ID,
					Priority: PreflightRepairCritical,
					Command:  "bd migrate",
					Note:     "Migrate the city's databases deliberately, with one designated migrator, then bring every gc and bd binary on this host to the matching version. Never let an ad hoc build migrate them on connect.",
				})
			}
		case PreflightCheckContractShape:
			if check.State == PreflightCheckFail || check.State == PreflightCheckWarn {
				steps = append(steps, PreflightRepairStep{
					CheckID:  check.ID,
					Priority: PreflightRepairRecommended,
					Command:  "bd bootstrap",
					Note:     "Rewrite metadata to the canonical backend field shape.",
				})
			}
		}
	}
	return steps
}
