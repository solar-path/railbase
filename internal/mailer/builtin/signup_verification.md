---
subject: Verify your {{ site.name }} email
from: "{{ site.from }}"
---

# Welcome to {{ site.name }}!

Hi {{ user.email }},

Thanks for signing up. Click the link below to verify your email address. The link expires in 24 hours.

[Verify email]({{ verify_url }})

If you didn't sign up, you can safely ignore this email.

— The {{ site.name }} team
