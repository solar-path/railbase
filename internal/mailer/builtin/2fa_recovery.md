---
subject: Your {{ site.name }} 2FA recovery codes
from: "{{ site.from }}"
---

# Your two-factor recovery codes

Hi {{ user.email }},

You enabled two-factor authentication on your {{ site.name }} account. Save the recovery codes below in a safe place — each code can be used **once** if you lose access to your authenticator app.

{{ recovery_codes }}

Treat these codes like passwords. Anyone with one of them can sign in to your account.

If you didn't enable 2FA, please contact support immediately.

— The {{ site.name }} team
