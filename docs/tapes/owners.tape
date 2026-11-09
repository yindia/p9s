# p9s: violations by owner and by team. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/owners.gif.
Output docs/image/owners.gif
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

Type "p9s --context kind-p9s --group-label team -c v"
Enter
Sleep 4.5s

# A Deployment's violations are one row; enter expands the group
Enter
Sleep 3s
Escape
Sleep 1s

# o: one row per violation, and back
Type "o"
Sleep 3s
Type "o"
Sleep 1.5s

# t: group by the owners' team label instead
Type "t"
Sleep 3.5s
Type "/payments"
Enter
Sleep 4s
Ctrl+C
