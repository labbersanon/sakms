package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/apidto"
)

// TestHandlerDTOMirrorNoDrift guards the seam TestNoDrift (internal/apidto/gen)
// does not: that gate proves apidto ↔ generated TypeScript stay in sync, but
// nothing proves the handler-local request/response structs actually used on
// the wire (authStatusResponse, oidcStatusResponse, upsertConnectionRequest,
// …) stay in sync with their apidto mirror types. Those two-struct pairs are a
// DELIBERATE choice (see internal/apidto/dto.go's header: apidto is a parallel
// EXPORTED copy so handlers aren't wired directly to it, avoiding lockout-risky
// auth refactors). Because they're byte-identical today by hand, a future
// silent rename/retype on one side without the other would ship a wrong-but-
// green TypeScript contract on the auth/settings surface. This test is the
// drift catcher for that gap.
//
// It compares the two structs STRUCTURALLY (reflect over fields), keyed by JSON
// name, capturing each field's Go type and omitempty flag — not by marshaling
// a fixture. That is deliberate: json.Marshal of an APIKey *string set to &"x"
// and an APIKey string set to "x" both emit {"apiKey":"x"}, so a marshal-and-
// compare test would stay green if upsertConnectionRequest.APIKey (the
// three-state secret field, Guardrail #5) were flipped from *string to string —
// exactly the highest-stakes drift. Comparing field.Type.String() catches it.
//
// Keying by JSON name (not field order) means a harmless field reorder — which
// changes neither the wire shape nor the generated TS — correctly does not
// fail, and omitempty differences are caught without needing zero-value
// fixtures.
//
// SCOPE: handler-local FULL MIRRORS only. apidto's curated domain-subset types
// (Proposal, Candidate, DiscoverItem, Grab, ConnectionSummary, NetscanFinding)
// are intentionally NOT field-identical to their internal/domain sources (see
// their dto.go doc comments) and are deliberately excluded here — they have no
// handler-local mirror to compare against.
func TestHandlerDTOMirrorNoDrift(t *testing.T) {
	cases := []struct {
		name    string
		handler any
		mirror  any
	}{
		{"authStatusResponse", authStatusResponse{}, apidto.AuthStatusResponse{}},
		{"authCredentialsRequest/SetupRequest", authCredentialsRequest{}, apidto.SetupRequest{}},
		{"authSetupResponse/SetupResponse", authSetupResponse{}, apidto.SetupResponse{}},
		{"authModeResponse", authModeResponse{}, apidto.AuthModeResponse{}},
		{"authModeRequest", authModeRequest{}, apidto.AuthModeRequest{}},
		{"oidcStatusResponse/OIDCStatusResponse", oidcStatusResponse{}, apidto.OIDCStatusResponse{}},
		{"oidcConfigRequest/OIDCConfigRequest", oidcConfigRequest{}, apidto.OIDCConfigRequest{}},
		{"upsertConnectionRequest/ConnectionUpsertRequest", upsertConnectionRequest{}, apidto.ConnectionUpsertRequest{}},
		{"ConnectionTestRequest", ConnectionTestRequest{}, apidto.ConnectionTestRequest{}},
		{"ConnectionTestResult", ConnectionTestResult{}, apidto.ConnectionTestResult{}},
		{"addAllowlistTagRequest/AllowlistAddRequest", addAllowlistTagRequest{}, apidto.AllowlistAddRequest{}},
		{"dismissSetupRequest/DismissSetupRequest", dismissSetupRequest{}, apidto.DismissSetupRequest{}},
		{"applyProposalRequest/DedupApplyRequest", applyProposalRequest{}, apidto.DedupApplyRequest{}},
		// applyBatchRequest/ApplyBatchResultItem/ApplyBatchResponse are excluded:
		// each has a nested named-type field (a []applyBatchItem or a curated
		// *proposals.Proposal) whose Go type string differs from its apidto
		// mirror by construction, same reason Proposal/Candidate are excluded.
		// applyBatchItem is all primitives, so it IS a full mirror worth guarding.
		{"applyBatchItem/ApplyBatchItem", applyBatchItem{}, apidto.ApplyBatchItem{}},
		{"repickProposalRequest/RepickRequest", repickProposalRequest{}, apidto.RepickRequest{}},
		{"libraryTagEntry/TagEntry", libraryTagEntry{}, apidto.TagEntry{}},
		{"libraryTrackedItem/TrackedItem", libraryTrackedItem{}, apidto.TrackedItem{}},
		// settings.go
		{"aiModelResponse/AIModelResponse", aiModelResponse{}, apidto.AIModelResponse{}},
		{"aiModelRequest/AIModelRequest", aiModelRequest{}, apidto.AIModelRequest{}},
		{"aiProviderResponse/AIProviderResponse", aiProviderResponse{}, apidto.AIProviderResponse{}},
		{"aiProviderRequest/AIProviderRequest", aiProviderRequest{}, apidto.AIProviderRequest{}},
		// library.go
		{"libraryRootFolderResponse/LibraryRootFolderResponse", libraryRootFolderResponse{}, apidto.LibraryRootFolderResponse{}},
		{"libraryRootFolderRequest/LibraryRootFolderRequest", libraryRootFolderRequest{}, apidto.LibraryRootFolderRequest{}},
		{"qualityPrefsResponse/QualityPrefsResponse", qualityPrefsResponse{}, apidto.QualityPrefsResponse{}},
		{"qualityPrefsRequest/QualityPrefsRequest", qualityPrefsRequest{}, apidto.QualityPrefsRequest{}},
		{"namingPresetResponse/NamingPresetResponse", namingPresetResponse{}, apidto.NamingPresetResponse{}},
		{"namingPresetRequest/NamingPresetRequest", namingPresetRequest{}, apidto.NamingPresetRequest{}},
		{"phashThresholdResponse/PHashThresholdResponse", phashThresholdResponse{}, apidto.PHashThresholdResponse{}},
		{"phashThresholdRequest/PHashThresholdRequest", phashThresholdRequest{}, apidto.PHashThresholdRequest{}},
		{"confidenceThresholdResponse/ConfidenceThresholdResponse", confidenceThresholdResponse{}, apidto.ConfidenceThresholdResponse{}},
		{"confidenceThresholdRequest/ConfidenceThresholdRequest", confidenceThresholdRequest{}, apidto.ConfidenceThresholdRequest{}},
		{"identifyEnabledResponse/IdentifyEnabledResponse", identifyEnabledResponse{}, apidto.IdentifyEnabledResponse{}},
		{"identifyEnabledRequest/IdentifyEnabledRequest", identifyEnabledRequest{}, apidto.IdentifyEnabledRequest{}},
		// recheck.go
		{"recheckIntervalResponse/RecheckIntervalResponse", recheckIntervalResponse{}, apidto.RecheckIntervalResponse{}},
		{"recheckIntervalRequest/RecheckIntervalRequest", recheckIntervalRequest{}, apidto.RecheckIntervalRequest{}},
		// rename.go
		{"kidsRootPathResponse/KidsRootPathResponse", kidsRootPathResponse{}, apidto.KidsRootPathResponse{}},
		{"kidsRootPathRequest/KidsRootPathRequest", kidsRootPathRequest{}, apidto.KidsRootPathRequest{}},
		// apikey.go
		{"apikeyRegenerateResponse/APIKeyRegenerateResponse", apikeyRegenerateResponse{}, apidto.APIKeyRegenerateResponse{}},
		// trakt.go
		{"traktCredentialsRequest/TraktCredentialsRequest", traktCredentialsRequest{}, apidto.TraktCredentialsRequest{}},
		{"traktStatus/TraktStatusResponse", traktStatus{}, apidto.TraktStatusResponse{}},
		{"traktDeviceStartResponse/TraktDeviceStartResponse", traktDeviceStartResponse{}, apidto.TraktDeviceStartResponse{}},
		{"traktDevicePollResponse/TraktDevicePollResponse", traktDevicePollResponse{}, apidto.TraktDevicePollResponse{}},
		{"traktWatchlistItem/TraktWatchlistItem", traktWatchlistItem{}, apidto.TraktWatchlistItem{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := jsonFieldSpecs(reflect.TypeOf(tc.handler))
			m := jsonFieldSpecs(reflect.TypeOf(tc.mirror))
			if !reflect.DeepEqual(h, m) {
				t.Errorf("handler struct %T has drifted from apidto mirror %T\n"+
					"  handler: %v\n"+
					"  apidto : %v\n"+
					"These two must stay wire-identical (see this test's doc). Reconcile the field(s) above.",
					tc.handler, tc.mirror, h, m)
			}
		})
	}
}

// fieldSpec is one field's wire-relevant identity: its Go type and whether it
// marshals with omitempty. Keyed by JSON name in the map jsonFieldSpecs builds.
type fieldSpec struct {
	goType    string
	omitempty bool
}

// jsonFieldSpecs reflects a struct type into a map of json-name → fieldSpec,
// skipping fields tagged json:"-". Panics on a non-struct (test-only helper).
func jsonFieldSpecs(t reflect.Type) map[string]fieldSpec {
	if t.Kind() != reflect.Struct {
		panic("jsonFieldSpecs: not a struct: " + t.String())
	}
	specs := make(map[string]fieldSpec, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "-" {
			continue // explicitly not serialized
		}
		if name == "" {
			name = f.Name // no json tag → Go field name is the wire key
		}
		omitempty := false
		for _, opt := range parts[1:] {
			if opt == "omitempty" {
				omitempty = true
			}
		}
		specs[name] = fieldSpec{goType: f.Type.String(), omitempty: omitempty}
	}
	return specs
}
