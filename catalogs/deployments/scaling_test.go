/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package deployments

import (
	"errors"
	"fmt"
	"testing"
)

func TestHPACooldownBounds(t *testing.T) {
	for _, value := range []int64{-1, 0, 1800, 3600, 3601} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			err := (&HPA{CooldownPeriod: &value}).Validate()
			invalid := value < 0 || value > 3600
			if errors.Is(err, ErrInvalidValue) != invalid {
				t.Fatalf("CooldownPeriod=%d: got %v, invalid=%t", value, err, invalid)
			}
		})
	}
	if err := (&HPA{}).Validate(); err != nil {
		t.Fatalf("Omitted cooldown: %v", err)
	}
}
