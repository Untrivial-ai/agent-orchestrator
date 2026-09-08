//go:build chatui_regression

package chat_test

import "testing"

// TestChatUIRegressionDraftDeliveryRecoveryIsAtMostOnce selects the ordinary
// package contracts that prove a restored Chat draft cannot redispatch an
// ambiguous steer or inline edit after either an in-process retry or controller
// restart. The opt-in runner invokes this stable name alongside MQA-04.
func TestChatUIRegressionDraftDeliveryRecoveryIsAtMostOnce(t *testing.T) {
	t.Run("steer", TestReservedSteerStaysUncertainAcrossRetryAndRestart)
	t.Run("inline edit", TestEditCompletionGapStaysUncertainAcrossRetryAndControllerRestart)
}
