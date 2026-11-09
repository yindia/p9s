# p9s: the end-to-end demo for the top of the README. Render with
# `make tapes TAPES=docs/tapes/demo.tape` (needs vhs and `make e2e-up`);
# writes docs/image/demo.gif.
#
# One story: something was blocked; whose is it and how is it fixed; what
# would enforcing break; is the policy layer healthy; hand it to security.
Output docs/image/demo.gif
Require p9s
Require kubectl
Set Shell zsh
Set Theme "Catppuccin Mocha"
Set FontSize 15
Set LineHeight 1.25
Set Width 1600
Set Height 1000
Set Padding 28
Set Margin 24
Set MarginFill "#11111b"
Set WindowBar Colorful
Set BorderRadius 10
Set TypingSpeed 90ms

# A fresh denial for the recent-events tab, off camera.
Hide
Type "kubectl --context kind-p9s -n default create deployment p9s-demo-denied --image=nginx:latest --dry-run=server >/dev/null 2>&1; clear"
Enter
Sleep 3s
Show

# 1. Is the policy layer working, and what should I look at first?
Type "p9s --context kind-p9s"
Enter
Sleep 5s

# 2. What was just blocked, and by which policy?
Type ":v"
Enter
Sleep 2s
Type "]"
Sleep 4.5s
Type "["
Sleep 1.5s

# 3. Whose is it? Violations grouped by the owning Deployment.
Enter
Sleep 4s
Escape
Sleep 1s

# 4. How do I fix it? The detail carries the policy's own remediation.
Type "o"
Type "/disallow-privilege-escalation"
Enter
Sleep 1.5s
Type "d"
Sleep 6s
Escape
Escape
Sleep 1s

# 5. What would break if this policy enforced?
Type ":impact require-team-label"
Enter
Sleep 5s
Escape
Sleep 1s

# 6. Can I trust any of this? Webhooks, certificates, controllers, audits.
Type ":health"
Enter
Sleep 4s
Enter
Sleep 4.5s
Escape
Sleep 1s

# 7. The same tables outside the TUI, for scripts and CI.
Ctrl+C
Sleep 1s
Type "p9s --context kind-p9s get v -n p9s-demo | head -8"
Enter
Sleep 5s

# 8. Hand it to security: a report, and a CI gate.
Type "p9s --context kind-p9s report --format md | head -24"
Enter
Sleep 6s
Type "p9s --context kind-p9s report --fail-on critical > /dev/null; echo exit=$?"
Enter
Sleep 5s
