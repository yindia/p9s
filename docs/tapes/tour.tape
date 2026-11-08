# p9s: a tour of the TUI. Render with `make tapes` (needs vhs and `make e2e-up`); writes docs/image/tour.gif.
Output docs/image/tour.gif
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

Type "p9s --context kind-p9s"
Enter
Sleep 4.5s

# Overview: walk the blocks, open enforcement candidates, come back
Type "jj"
Sleep 1.5s
Enter
Sleep 3s
Escape
Sleep 1.5s

# Policies across engines; drill into one policy's violations
Type "/requiredlabels"
Enter
Sleep 1.5s
Enter
Sleep 3s
Escape 2
Sleep 750ms

# Scope every view to one namespace, then jump to an offending object
Type ":ns kube-system"
Enter
Type ":v"
Enter
Sleep 3s
Type "g"
Sleep 3s
Escape
Type "0"
Sleep 1.5s

# Sorting and column filters
Type ":pol"
Enter
Type "N"
Sleep 1.5s
Type "V"
Sleep 1.5s
Type "/mode:audit engine:kyverno"
Enter
Sleep 3s
Escape
Type ":v"
Enter
Sleep 1.5s
# grouped by owner: enter expands a Deployment's violations
Enter
Sleep 3s
Escape
Type "o"
Sleep 1.5s
Type "c"
Sleep 1.5s

# Gatekeeper templates with highlighted rego
Type ":gk/ct"
Enter
Type "/allowedrepos"
Enter
Type "d"
Sleep 4.5s
Escape

# Constraints and their match/parameters/violations
Type ":gk/c"
Enter
Down 2
Type "d"
Sleep 4.5s
Escape

# Kyverno: CEL policies, reports, mutations
Type ":kyv/vpol"
Enter
Sleep 1.5s
Type "d"
Sleep 4.5s
Escape
Type ":kyv/polr"
Enter
Sleep 3s
Type ":mut"
Enter
Sleep 3s

# Health dashboard: panels, webhook detail
Type ":health"
Enter
Sleep 3s
Enter
Sleep 4.5s
Escape
Tab
Sleep 1.5s

Type "?"
Sleep 3s
Type "?"
Sleep 2s
Ctrl+C
