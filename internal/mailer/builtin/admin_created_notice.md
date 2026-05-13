---
subject: New administrator added to {{ site.name }}
from: "{{ site.from }}"
---

# New admin account on {{ site.name }}

Hi {{ recipient.email }},

A new administrator account has just been created on the **{{ site.name }}** Railbase instance you also administer.

- **New admin email:** {{ new_admin.email }}
- **Created at:** {{ event.at }}
- **Created by:** {{ event.created_by }}
- **Via:** {{ event.via }}

If you authorised this change, no action is needed.

If you didn't — this is a possible compromise. Review the audit log immediately and revoke the new account if necessary:

- **Admin UI:** {{ admin_url }}
- **Audit log:** {{ admin_url }}/audit

— The {{ site.name }} team
