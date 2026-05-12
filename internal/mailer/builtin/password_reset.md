---
subject: Reset your {{ site.name }} password
from: "{{ site.from }}"
---

# Password reset request

Hi {{ user.email }},

We received a request to reset your password. Click the link below to set a new one. The link expires in 1 hour.

[Reset password]({{ reset_url }})

If you didn't request this, your account is safe — you can ignore this email.

— The {{ site.name }} team
