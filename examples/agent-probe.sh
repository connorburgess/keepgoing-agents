#!/bin/sh
# Example probe command. Exit 0 when the agent is usable right now.
# LOOPTENDER_PROBE_TIER is scheduled, exact, or manual; LOOPTENDER_PROBE_MODEL
# is the model to check. Both also arrive under their earlier KEEPGOING_ names. Print {"ok": false, "errorClass": "rate_limit"} or
# use a mapped exit code to say why it is not usable.
exit 0
