---
subject: Confirm your new {{ site.name }} email
from: "{{ site.from }}"
---

# Confirm your new email address

Hi,

Someone (hopefully you) asked to change the email on your {{ site.name }} account to **{{ user.new_email }}**.

Click the link below to confirm. The link expires in 24 hours.

[Confirm new email]({{ confirm_url }})

If this wasn't you, please change your password immediately — your account may be compromised.

— The {{ site.name }} team
