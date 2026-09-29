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
#
# The answer to a question asked in Slack is read by a second guardrail as well,
# below: the consent guard's.

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

# The consent guard's guardrail. The answer to a question asked in Slack is read
# by it once the answer guard's has read it, before the asker reads it in the
# channel (agents/primary/internal/consentguard). It is a guardrail of its own,
# and the one above is left as it is: the answer guard calls that one with a single
# text and no grounding source, and takes a topic or a contextual grounding filter
# that acted for a response it cannot use, which it holds as unchecked
# (answerguard.assess); and a change to that one makes a new version for every
# answer the service reads. Only answers are read here too: the output side, never
# the input, in the region, with no cross-region profile.
#
# Two things are set up in it, and nothing else:
#
# - Contextual grounding, with both filters. The consent guard sends what the
#   run's tools returned as the grounding source, the asker's words as the query
#   and the answer as the content to guard, with the full output scope, so that
#   both scores come back whether or not they were found wanting. The thresholds
#   here are a floor. The guard holds an answer scored under the higher of them
#   and its own minimums (consentguard.DefaultMinGrounding and DefaultMinRelevance),
#   which are the ones to tune, in code, with no new version of this guardrail.
#   Bedrock takes 100,000 characters of source, 1,000 of query and 5,000 of
#   response; the guard sends no more.
# - Denied topics. The provider gives a topic no action of its own to set, so the
#   guardrail blocks on one, as far as it blocks anything; the guard reads the
#   findings, never the action, and holds the answer for its owner's review
#   instead of showing the blocked message. They are the topics that go to no
#   channel, whoever consented: they stand beside the relevance check's categories
#   (relevance.Category) that are about people, and the credentials that are
#   masked. They are the owner's to change, in local.consent_denied_topics, and
#   each change is a new version of this guardrail.
#
# The classic tier is used: the standard tier needs a cross-region profile, and
# a guardrail that leaves the region is one the service does not run.

locals {
  # A definition is at most 200 characters and an example at most 100, and there
  # are at most five examples of a topic (the classic tier's limits).
  consent_denied_topics = {
    "Credentials" = {
      definition = "Statements that reveal the value of a password, passcode, API key, access token, private key, recovery code or other secret that grants access."
      examples = [
        "The production database password is",
        "Here is the API key for the payments account",
        "Log in with this token",
        "The recovery codes for the admin account are",
        "Paste this private key into the config",
      ]
    }
    "Compensation and HR records" = {
      definition = "Pay, bonus, equity, performance reviews, disciplinary action, termination or immigration status of a named individual."
      examples = [
        "Her salary is",
        "He was put on a performance improvement plan",
        "She was terminated last month",
        "Their equity grant is",
        "His visa status is",
      ]
    }
    "Health and personal life" = {
      definition = "Medical conditions, treatment, mental health, family matters or relationships of a named individual."
      examples = [
        "She is on medical leave for surgery",
        "His divorce was finalized",
        "They are expecting a baby",
        "He is being treated for depression",
      ]
    }
  }
}

resource "aws_bedrock_guardrail" "consent_answers" {
  name        = "${local.name}-consent-answers"
  description = "Reads the answer to a question asked in Slack against what the run read, and for topics that go to no channel."
  # The guard reads what was found, never the action, so neither message is
  # shown; both are required all the same.
  blocked_input_messaging   = "Oryxa's consent guard held this."
  blocked_outputs_messaging = "Oryxa's consent guard held this answer."

  contextual_grounding_policy_config {
    filters_config {
      type      = "GROUNDING"
      threshold = 0.5
    }
    filters_config {
      type      = "RELEVANCE"
      threshold = 0.3
    }
  }

  topic_policy_config {
    tier_config {
      tier_name = "CLASSIC"
    }

    dynamic "topics_config" {
      for_each = local.consent_denied_topics
      content {
        name       = topics_config.key
        definition = topics_config.value.definition
        examples   = topics_config.value.examples
        type       = "DENY"
      }
    }
  }
}

# The numbered version the service pins, as for the guardrail above.
resource "aws_bedrock_guardrail_version" "consent_answers" {
  guardrail_arn = aws_bedrock_guardrail.consent_answers.guardrail_arn
  description   = "The agents service's consent guard."
  skip_destroy  = true

  lifecycle {
    replace_triggered_by = [aws_bedrock_guardrail.consent_answers]
  }
}

locals {
  # What the task role is allowed to apply, and what the task is told, for the
  # consent guard: for compute.tf to add to the answer guard's policy statement
  # and to the agents service's environment. The service refuses to start with
  # one of the two set and not the other; this sets both.
  consent_guardrail_arn = aws_bedrock_guardrail.consent_answers.guardrail_arn
  consent_guardrail_environment = {
    AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID      = aws_bedrock_guardrail.consent_answers.guardrail_id
    AGENTS_PRIMARY_CONSENT_GUARDRAIL_VERSION = aws_bedrock_guardrail_version.consent_answers.version
  }
}
