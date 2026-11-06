// Package register blank-imports restore provider packages so processors register via init.
package register

import (
	_ "github.com/StorX2-0/Backup-Tools/restore/google"
	_ "github.com/StorX2-0/Backup-Tools/restore/microsoft"
)
