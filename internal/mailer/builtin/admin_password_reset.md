---
subject: Reset your {{ site.name }} admin password
from: "{{ site.from }}"
---

# Password reset requested

A password reset was requested for **{{ admin.email }}** on **{{ site.name }}**.

If you made this request, click the link below to set a new password:

{{ reset_url }}

This link is valid for **{{ ttl_min }} minutes** and can be used **once**.

## Didn't request this?

If you did NOT request a password reset, you can safely ignore this email — your password has not been changed.

If you receive multiple reset emails you didn't request, or if you suspect your account has been compromised, contact your team immediately and:

- Rotate your password via the CLI: `railbase admin reset-password {{ admin.email }}`
- Review recent audit-log entries for unfamiliar activity
- Revoke any sessions you don't recognise in the admin UI

— The {{ site.name }} team
