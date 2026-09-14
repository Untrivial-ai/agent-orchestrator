//go:build chatui_regression

package chat_test

import "testing"

// TestChatUIRegressionDraftDeliveryRecoveryIsAtMostOnce selects the ordinary
// package contracts for both duplicate prevention and eventual recovery across
// controller restart. The opt-in runner invokes this stable name alongside MQA-04.
func TestChatUIRegressionDraftDeliveryRecoveryIsAtMostOnce(t *testing.T) {
	t.Run("steer", TestReservedSteerStaysUncertainAcrossRetryAndRestart)
	t.Run("inline edit", TestEditCompletionGapStaysUncertainAcrossRetryAndControllerRestart)
	t.Run("reserved edit resumes", TestReservedEditRecoversAfterControllerStopAndResume)
	t.Run("completed edit repairs receipt", TestCompletedEditRepairsReceiptAfterControllerRestart)
}
