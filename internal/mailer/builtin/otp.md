---
subject: Your {{ site.name }} sign-in code
from: "{{ site.from }}"
---

# Your sign-in code

Hi {{ user.email }},

Use the following code to finish signing in to {{ site.name }}:

**{{ otp_code }}**

Or [click here to sign in]({{ magic_url }}) — single-use link.

The code expires in 10 minutes. If you didn't try to sign in, you can safely ignore this email.

— The {{ site.name }} team
