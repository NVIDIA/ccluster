// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// This file only fences ui/ off from the github.com/NVIDIA/ccluster module: some npm
// packages in ui/node_modules ship .go files, which ./... patterns would otherwise build,
// vet and lint. ui/ holds no Go code.
module github.com/NVIDIA/ccluster/ui

go 1.26.9
