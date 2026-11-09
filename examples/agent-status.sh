#!/bin/sh
# Example status command for the process adapter. Replace with your own.
# Print a JSON status object on stdout. Exit 75 for a rate limit and 77 for
# an auth failure when your tool reports them through exit codes.
printf '{"state":"idle","queued":0,"background":0}\n'
