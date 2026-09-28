package setup

// YesUI is ui as --yes wraps it, for the tests outside the package that
// drive a run over a terminal the way `curio up --yes` does.
func YesUI(ui UI) UI { return yesUI{ui} }
