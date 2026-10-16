package fakecentral

// SetStatus sets an enrollment's status (ACTIVE, COMPLETED, CANCELLED).
func (c *Central) SetStatus(enrollment, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if o := c.objects[enrollment]; o != nil {
		o["status"] = status
	}
}

// Backdate sets updatedAt on an object and what is under it: for a tracked entity,
// its enrollments and their events; for an enrollment, its events. Data on a real
// central cannot be aged; the fake's can, for retention tests.
func (c *Central) Backdate(uid, updatedAt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.order {
		o := c.objects[u]
		if u == uid || o["trackedEntity"] == uid || o["enrollment"] == uid {
			o["updatedAt"] = updatedAt
		}
	}
}
