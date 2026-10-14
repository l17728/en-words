package agent

// These are teaching policies from S-06/S-07, not platform magic numbers.
func FeedbackPostponeLimit() int { return 3 }
func FeedbackPostponeAllowed(successfulThisMonth int) bool {
	return successfulThisMonth < FeedbackPostponeLimit()
}
func FeedbackBoostCapacity() int { return 20 }
