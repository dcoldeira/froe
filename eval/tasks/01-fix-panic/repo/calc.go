package evaltask

// Average returns the mean of nums.
func Average(nums []int) int {
	total := 0
	for _, n := range nums {
		total += n
	}
	return total / len(nums)
}
