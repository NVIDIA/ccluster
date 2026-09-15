# Copyright (c) CentML Inc. All Rights Reserved.
"""Defines the interfaces for deployment planner with pydantic"""

from __future__ import annotations

from typing import List, Literal, Optional

from pydantic import BaseModel, ConfigDict, Field


class CServeRecipe(BaseModel):
    """Base class for deployment planner"""

    # Huggingface model name
    model: str
    revision: str | None = None
    # Whether to use the model as an embedding model
    is_embedding_model: bool = False

    # number of GPUs to shard the model using tensor parallelism
    tensor_parallel_size: int
    # number of GPUs to shard the model using pipeline parallelism
    pipeline_parallel_size: int

    # page attention page block size
    block_size: Literal[16, 32] = 32
    # page attention swap to CPU space limit
    swap_space: int = Field(0, ge=0)
    # maximum ratio of GPU memory to use per device
    gpu_mem_util: float = Field(0.95, ge=0, le=1)

    # maximum number of sequences to process in one batch
    max_num_seqs: int = 256

    # number of layers to offload to CPU to conserve GPU memory
    offloading_num: int = 0

    # whether to use prefix caching
    use_prefix_caching: Optional[bool] = False
    # whether to enable chunked prefill (mixing prefill and decode)
    use_chunked_prefill: Optional[bool] = False
    # size of each chunk for chunked prefill
    chunked_prefill_size: Optional[int] = None

    # whether to reduce framework level overhead through cuda graphs
    eager_execution: Optional[bool] = True
    # whether to use return multiple tokens for one scheduling step
    num_scheduler_steps: Optional[int] = 1

    # whether to use flashinfer kernels for attention
    use_flashinfer: bool = False

    # maximum number of tokens to process in one batch
    max_model_len: Optional[int] = Field(None, ge=128)

    # dtype of the model, auto means float16 or bfloat16 depending on the model downloaded from huggingface
    dtype: Literal["auto", "float16", "float32", "bfloat16"] = "auto"

    # whether to use a tokenizer that's different from the model
    tokenizer: Optional[str] = None

    # DEPRECATED: speculative decoding parameters, use spec_draft_model instead
    spec_proposer: Optional[Literal["draft", "prompt_lookup"]] = None
    # draft model for speculative decoding, for prompt_lookup, use [ngram]
    spec_draft_model: Optional[str] = None

    # number of steps to look ahead for speculative decoding, must be specified if spec_proposer is set
    spec_tokens: Optional[int] = None

    # window for prompt lookup, must be specified if spec_proposer is prompt_lookup
    spec_prompt_lookup_min: Optional[int] = Field(None, ge=1)
    # window for prompt lookup, must be specified if spec_proposer is prompt_lookup
    spec_prompt_lookup_max: Optional[int] = Field(None, ge=1)

    # seed for reproducibility with random sampling
    seed: int = 0


class PlanOutput(CServeRecipe):
    """outputs of deployment planner V2"""

    ngpus: int
    download_dir: Optional[str] = None
    dist_init_method: Optional[str] = None
    engine_managers: List[str] = ["localhost:6060"]
    distributed_executor_backend: Optional[str] = None


class CServeV2Recipe(BaseModel):
    """Inputs to start deployment"""

    # Allow extra fields to be transparently passed through to cserve
    model_config = ConfigDict(extra="allow")

    # Huggingface model name
    model: str

    # Optional revision name
    revision: str | None = None

    # Whether to use the model as an embedding model
    is_embedding_model: bool = False


class DeploymentInputV2(CServeV2Recipe):
    # System dependent parameters
    ngpus: int
    engine_managers: List[str] = []
    download_dir: Optional[str] = None
    tokenizer_mode: str = "auto"
    seed: int = 0
    dist_init_method: Optional[str] = None


class PlanOutputV2(BaseModel):
    """Outputs of the deployment planner"""

    deployment_input: DeploymentInputV2
