---
subject: Sign in to {{ site.name }}
from: "{{ site.from }}"
---

# Sign in to {{ site.name }}

Hi {{ user.email }},

Click the link below to sign in. It expires in 15 minutes and can only be used once.

[Sign in]({{ magic_url }})

If you didn't try to sign in, just ignore this email.

— The {{ site.name }} team
