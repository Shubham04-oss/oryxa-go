# The answer guard: every answer one member's agent gives another member is
# read by this guardrail before that member reads it (agents/primary/README.md,
# "Answer guard"). Only answers are read: the output side, never the input,
# and the guardrail runs here in the region, with no cross-region profile.
#
# The kinds that are always masked are anonymized, so an answer with only
# those is delivered as the guardrail masked it. Email addresses, phone
# numbers and addresses are detected and not acted on (NONE), which holds the
# answer for its member's review; the service reads that from the findings,
# not from the guardrail's action. Nothing else is set up: names, ages, user
# names, URLs, IP and MAC addresses, licence plates and vehicle numbers are
# too common in work answers. These lists are the service's own
# (answerguard.MaskedKinds and HeldKinds), and a test holds them equal.

locals {
  answer_guard_masked = [
    "PASSWORD", "AWS_ACCESS_KEY", "AWS_SECRET_KEY",
    "CREDIT_DEBIT_CARD_NUMBER", "CREDIT_DEBIT_CARD_CVV", "CREDIT_DEBIT_CARD_EXPIRY", "PIN",
    "INTERNATIONAL_BANK_ACCOUNT_NUMBER", "SWIFT_CODE",
    "US_BANK_ACCOUNT_NUMBER", "US_BANK_ROUTING_NUMBER", "US_SOCIAL_SECURITY_NUMBER",
    "US_INDIVIDUAL_TAX_IDENTIFICATION_NUMBER", "US_PASSPORT_NUMBER",
    "DRIVER_ID",
    "CA_HEALTH_NUMBER", "CA_SOCIAL_INSURANCE_NUMBER",
    "UK_NATIONAL_HEALTH_SERVICE_NUMBER", "UK_NATIONAL_INSURANCE_NUMBER", "UK_UNIQUE_TAXPAYER_REFERENCE_NUMBER",
  ]
  answer_guard_held = ["EMAIL", "PHONE", "ADDRESS"]
}

resource "aws_bedrock_guardrail" "agent_answers" {
  name        = "${local.name}-agent-answers"
  description = "Reads every answer one member's agent gives another: masks secrets and card, bank and ID numbers; finds emails, phone numbers and addresses for review."
  # Nothing is set up to block, so neither message is ever shown; both are
  # required all the same.
  blocked_input_messaging   = "Oryxa's answer guard held this."
  blocked_outputs_messaging = "Oryxa's answer guard held this answer."

  sensitive_information_policy_config {
    dynamic "pii_entities_config" {
      for_each = local.answer_guard_masked
      content {
        type           = pii_entities_config.value
        action         = "ANONYMIZE"
        input_enabled  = false
        output_enabled = true
        output_action  = "ANONYMIZE"
      }
    }

    dynamic "pii_entities_config" {
      for_each = local.answer_guard_held
      content {
        type           = pii_entities_config.value
        action         = "NONE"
        input_enabled  = false
        output_enabled = true
        output_action  = "NONE"
      }
    }
  }
}

# The numbered version the service pins. A change to the guardrail makes a
# new version, which the task's configuration then names; an old one is kept
# rather than deleted, so a task still naming it keeps working until it rolls.
resource "aws_bedrock_guardrail_version" "agent_answers" {
  guardrail_arn = aws_bedrock_guardrail.agent_answers.guardrail_arn
  description   = "The agents service's answer guard."
  skip_destroy  = true

  lifecycle {
    replace_triggered_by = [aws_bedrock_guardrail.agent_answers]
  }
}
