---
subject: New sign-in to your {{ site.name }} account
from: "{{ site.from }}"
---

# New device sign-in

Hi {{ user.email }},

We noticed a sign-in to your {{ site.name }} account from a device we haven't seen before:

- **When:** {{ event.at }}
- **From (network):** {{ event.ip_class }}
- **Browser / device:** {{ event.user_agent }}

If this was you, no action needed.

If it wasn't, please [reset your password]({{ reset_url }}) immediately and review your active sessions.

— The {{ site.name }} team
