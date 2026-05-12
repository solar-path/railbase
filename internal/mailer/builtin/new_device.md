---
subject: New sign-in to your {{ site.name }} account
from: "{{ site.from }}"
---

# New sign-in detected

Hi {{ user.email }},

We noticed a new sign-in to your {{ site.name }} account:

- **When:** {{ event.at }}
- **From IP:** {{ event.ip }}
- **Device:** {{ event.user_agent }}

If this was you, no action needed.

If it wasn't, [reset your password]({{ reset_url }}) right away and review your sessions.

— The {{ site.name }} team
