Feature: CTRL-C Then Tab Close — Abort the Paused Ritual
  As a Ruler who CTRL-C'd a running ritual
  I want closing the ritual tab to abort the ritual
  So that the court's single-flight latch is released and new rituals run immediately

  Scenario: Closing a paused ritual tab aborts the ritual
    Given a court is active with a recorded CancelTab
    And a ritual tab exists for edict 647
    And the ritual is paused (ChatMode = true)
    When the Ruler types ":q"
    Then the ritual tab is closed
    And the ritual is aborted through the court

  Scenario: A running (not paused) ritual tab aborts without a spurious resume
    Given a court is active with a recorded CancelTab
    And a ritual tab exists for edict 647
    And the ritual is not paused
    When the Ruler types ":q"
    Then the ritual tab is closed
    And the ritual is aborted through the court
    And the ritual was not resumed

  Scenario: A streaming ritual tab is refused, leaving the ritual alone
    Given a court is active with a recorded CancelTab
    And a ritual tab exists for edict 647
    And the ritual is actively streaming
    When the Ruler types ":q"
    Then the ritual tab is still open
    And the ritual was not aborted
