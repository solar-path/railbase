---
subject: Welcome to {{ site.name }} — your admin account is ready
from: "{{ site.from }}"
---

# Welcome, {{ admin.email }}

Your administrator account on **{{ site.name }}** has been created.

- **Login URL:** {{ admin_url }}
- **Email:** {{ admin.email }}
- **Created at:** {{ event.at }}
- **Via:** {{ event.via }}

Use the email above and the password you set during account creation to sign in.

## Next steps

- Set up two-factor authentication from the admin UI.
- Review the audit log to confirm this is the only recent admin activity.
- If you didn't create this account, contact your team immediately and rotate your password.

— The {{ site.name }} team
