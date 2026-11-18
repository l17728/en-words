package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRepeatEvaluationUsesSpeechNormalizationForTarget(t *testing.T) {
	app, _ := newTestApp(t)
	require.Equal(t, "matched", app.evaluateRepeatWithVariants(1, "have sth. in common", "have something in common"))
	require.Equal(t, "matched", app.evaluateRepeatWithVariants(1, "ASAP", "A S A P"))
	require.Equal(t, "no_match", app.evaluateRepeatWithVariants(1, "have sth. in common", "have sb. in common"))
}
