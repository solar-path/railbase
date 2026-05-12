---
subject: Your {{ .Mode }} digest — {{ .Count }} updates
---

# Your {{ .Mode }} digest

You have **{{ .Count }} unread notifications** since your last digest.

{{ range .Items }}
* **{{ .Title }}**{{ if .Body }} — {{ .Body }}{{ end }}
{{ end }}

[View all notifications](/notifications)

You're getting this email because you have digest mode enabled. Change your preferences in your account settings.
