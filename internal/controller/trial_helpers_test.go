package controller

import "testing"

func TestEnvVarName(t *testing.T) {
	cases := map[string]string{
		"lr":            "HP_LR",
		"learning_rate": "HP_LEARNING_RATE",
		"dropout-rate":  "HP_DROPOUT_RATE",
		"batch.size":    "HP_BATCH_SIZE",
	}
	for in, want := range cases {
		if got := EnvVarName(in); got != want {
			t.Errorf("EnvVarName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHyperparamEnv(t *testing.T) {
	env := hyperparamEnv(map[string]string{"lr": "0.01"})
	if len(env) != 1 || env[0].Name != "HP_LR" || env[0].Value != "0.01" {
		t.Fatalf("unexpected env: %+v", env)
	}
}
