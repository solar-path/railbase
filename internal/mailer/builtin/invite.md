---
subject: You're invited to {{ site.name }}
from: "{{ site.from }}"
---

# {{ inviter.email }} invited you to {{ site.name }}

Hi,

{{ inviter.email }} added you to **{{ org.name }}** on {{ site.name }}.

Click the link below to accept and pick a password. The invite expires in 7 days.

[Accept invitation]({{ invite_url }})

If you don't recognise this invitation, ignore the email.

— The {{ site.name }} team
