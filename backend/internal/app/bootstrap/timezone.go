package bootstrap

import (
	"fmt"
	"log"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
)

// InitTimezone sets time.Local during bootstrap; subsequent assembly uses it to construct Calendar.
func InitTimezone(name string) error {
	if name == "" {
		name = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("invalid timezone %q: %w", name, err)
	}
	time.Local = location
	log.Printf("Timezone initialized: %s (UTC offset: %s)", name, timezone.NewCalendar(location).UTCOffset(time.Now()))
	return nil
}
