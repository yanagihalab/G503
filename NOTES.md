# Crossrefd Hysteresis Docker Experiment

This repository tracks the Docker experiment changes that were split out from
`.ignite-work/crossrefd`.

The contents cover:

- README updates for the five-chain Docker experiment.
- deterministic Ed25519 hysteresis checkpoint signing.
- the patch file needed to apply these changes back onto `crossrefd`.

Apply the patch from a `crossrefd` checkout with:

```bash
git apply patches/crossrefd-hysteresis-docker.patch
```
