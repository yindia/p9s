package fake_test

import (
	"testing"

	"github.com/yindia/p9s/internal/engine"
	"github.com/yindia/p9s/internal/engine/enginetest"
	"github.com/yindia/p9s/internal/engines/fake"
	"github.com/yindia/p9s/internal/engines/fake/fixtures"
)

func TestContract(t *testing.T) {
	enginetest.Run(t, enginetest.Harness{
		New: func(*testing.T) engine.Engine { return fixtures.Alpha() },
		NotInstalled: func(*testing.T) engine.Engine {
			return fake.New(fake.Options{Name: "absent"})
		},
	})
}

func TestContractMinimalCapabilities(t *testing.T) {
	enginetest.Run(t, enginetest.Harness{
		New: func(*testing.T) engine.Engine {
			return fake.New(fake.Options{Name: "bare", Installed: true, Version: "0"})
		},
	})
}
