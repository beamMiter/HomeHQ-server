"""Cloud Run function: switches billing off for ONE project when that project's
budget is exceeded. Follows Google's "Disable billing usage with notifications"
pattern, with three additions: it acts only on TARGET_PROJECT, it starts in
simulation mode, and anything it cannot read is ignored.

Switching billing off stops every service in the project, free-tier ones
included, and Google warns that resources may be irretrievably deleted. Keep
the data elsewhere (the database is on Neon, files on R2) so the project can be
rebuilt from the repository.

Environment:
  TARGET_PROJECT  the project whose billing may be switched off (required)
  SIMULATE        anything but "false" only logs what would happen (default)
"""

import logging
import os

import functions_framework
from google.cloud import billing_v1

from decision import decide

# The functions framework installs its own log handler, which makes
# logging.basicConfig a no-op, so INFO lines ("no action: ...") never showed.
# Set the level on this logger itself.
log = logging.getLogger("killswitch")
log.setLevel(logging.INFO)


@functions_framework.cloud_event
def stop_billing(cloud_event):
    project = os.environ["TARGET_PROJECT"]
    simulate = os.environ.get("SIMULATE", "true").strip().lower() != "false"

    decision = decide(cloud_event.data)
    if not decision.disable:
        log.info("no action: %s", decision.reason)
        return

    name = f"projects/{project}"
    client = billing_v1.CloudBillingClient()

    try:
        enabled = client.get_project_billing_info(name=name).billing_enabled
    except Exception as err:  # as Google's sample: if unsure, assume it is on and try
        # Expected under the narrow role this runs with: it may change a
        # project's billing link but not read it. One line, no stack trace, so
        # the line that matters ("BILLING SWITCHED OFF") is not buried.
        log.warning("could not read billing state of %s (%s); going on as if it is enabled", name, type(err).__name__)
        enabled = True
    if not enabled:
        log.info("billing is already off for %s", name)
        return

    if simulate:
        log.warning("SIMULATION: would switch billing off for %s (%s)", name, decision.reason)
        return

    client.update_project_billing_info(
        name=name,
        project_billing_info=billing_v1.ProjectBillingInfo(billing_account_name=""),
    )
    log.critical("BILLING SWITCHED OFF for %s (%s)", name, decision.reason)
