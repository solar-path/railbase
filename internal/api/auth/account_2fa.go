// User-facing 2FA status — Sprint 3 of the account-page roadmap.
//
//   GET /api/auth/2fa/status — { enrolled: bool }
//
// The four mutation endpoints (totp-enroll-start, totp-enroll-confirm,
// totp-disable, totp-recovery-codes) already exist under
// /api/collections/{name}/totp-* (see mfa_flow.go). Sprint 3's value
// add at the backend is just this single read endpoint: without it,
// the UI has to try enroll-start to learn whether the user is already
// enrolled (which has side effects — creates a pending enrollment).
//
// Lives on the global `/api/auth/...` namespace alongside /me — the
// status answer doesn't depend on which collection the user is in,
// just on the principal in ctx.
package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	authmw "github.com/railbase/railbase/internal/auth/middleware"
	"github.com/railbase/railbase/internal/auth/mfa"
	rerr "github.com/railbase/railbase/internal/errors"
)

// twoFAStatusHandler answers "is the caller TOTP-enrolled?" with a
// simple bool. When the TOTPEnrollments store isn't configured at all
// (operator turned 2FA off at boot), we return enrolled=false rather
// than 503 — the UI then knows to hide the 2FA section entirely, which
// is the operator-intended UX for a 2FA-disabled deployment.
func (d *Deps) twoFAStatusHandler(w http.ResponseWriter, r *http.Request) {
	p := authmw.PrincipalFrom(r.Context())
	if !p.Authenticated() {
		rerr.WriteJSON(w, rerr.New(rerr.CodeUnauthorized, "not signed in"))
		return
	}
	enrolled := false
	if d.TOTPEnrollments != nil {
		enr, err := d.TOTPEnrollments.Get(r.Context(), p.CollectionName, p.UserID)
		if err != nil && !errors.Is(err, mfa.ErrNotFound) {
			rerr.WriteJSON(w, rerr.Wrap(err, rerr.CodeInternal, "lookup enrollment"))
			return
		}
		// Active() flips to true only AFTER totp-enroll-confirm — a
		// pending-but-unconfirmed enrollment shouldn't be reported as
		// "enrolled" to the UI (the user could still abandon the QR
		// scan).
		enrolled = enr != nil && enr.Active()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"enrolled": enrolled})
}
