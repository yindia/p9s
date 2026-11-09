# p9s: the health dashboard. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/health.gif.
Output docs/image/health.gif
Require p9s
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

Type "p9s --context kind-p9s -c health"
Enter
Sleep 4.5s

# The fail-open webhook: why it is critical and what it lets through
Enter
Sleep 6s
Escape
Sleep 1s

# Controllers, audit and policies panels
Tab
Sleep 2.5s
Tab
Sleep 2.5s
Tab
Sleep 3.5s
Ctrl+C
