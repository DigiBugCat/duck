package command

import "github.com/DigiBugCat/duck/internal/manager"

func managerLine(extraArgs []string) string { return manager.Line(extraArgs) }
