# p9s: recent admission denials. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/admission-events.gif.
Output docs/image/admission-events.gif
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

# A fresh denial (dry-run: nothing is created)
Type "kubectl --context kind-p9s -n default create deployment p9s-tape --image=nginx:latest --dry-run=server; clear"
Enter
Sleep 3s

Type "p9s --context kind-p9s"
Enter
Sleep 4.5s

# Overview: the denials block, then open it
Type "jjj"
Sleep 3s
Enter
Sleep 3s
Enter
Sleep 4.5s
Escape
Escape

# :v current tab, then the recent tab and back
Type ":v"
Enter
Sleep 3s
Type "]"
Sleep 3s
Type "/engine:kyverno"
Enter
Sleep 3s
Escape
Type "["
Sleep 3s

Sleep 2s
Ctrl+C
