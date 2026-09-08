package sqlite

// eligibleScoringEventSQL is used only with the internal detection_events
// alias de. Human invalidations never delete raw evidence, but prevent it from
// earning fresh cross-match points or being recycled through PAT_003. A newer
// verdict replaces an older verdict. A direct label survives same-version
// re-analysis only when the sampled observation and evidence are identical.
// Paused playspacing observations are likewise retained, but are ineligible
// for newly computed history/aggregate findings (model.IsPlayspaceDetector).
const eligibleScoringEventSQL = `de.is_shadow = 0
 AND de.enforcement_weight > 0 AND de.severity > 0 AND de.confidence > 0
 AND de.detector_id NOT IN ('PAT_003', 'PAT_004', 'MOV_006', 'PAT_005')
 AND COALESCE((SELECT er.verdict FROM event_reviews er
   WHERE er.event_id = de.event_id OR
     (er.match_id = de.match_id AND er.player_id = de.player_id
      AND er.detector_id = de.detector_id AND er.detector_version = de.detector_version
      AND er.frame_index = de.frame_index AND er.timestamp = de.timestamp
      AND er.observed_value = COALESCE(de.observed_value,'')
      AND er.expected_range = COALESCE(de.expected_range,'')
      AND er.evidence_type = COALESCE(de.evidence_type,'') AND er.evidence_json = COALESCE(de.evidence_json,''))
   ORDER BY er.reviewed_at DESC, er.event_id DESC LIMIT 1), '') != 'no'
 AND NOT EXISTS (
   SELECT 1 FROM review_cases rc JOIN moderator_decisions md ON md.case_id = rc.case_id
   WHERE rc.match_id = de.match_id AND rc.player_id = de.player_id
     AND md.decision_id = (SELECT newest.decision_id FROM moderator_decisions newest
       WHERE newest.case_id = rc.case_id ORDER BY newest.decided_at DESC, newest.decision_id DESC LIMIT 1)
     AND (md.verdict = 'false_positive' OR EXISTS (
       SELECT 1 FROM json_each(COALESCE(NULLIF(md.detector_feedback,''),'[]')) feedback
       WHERE json_extract(feedback.value,'$.detector_id') = de.detector_id
         AND json_extract(feedback.value,'$.correct') = 'no')))
 AND NOT EXISTS (
   SELECT 1 FROM cross_match_review_cases rc JOIN moderator_decisions md ON md.case_id = rc.case_id
   WHERE rc.player_id = de.player_id
     AND EXISTS (SELECT 1 FROM json_each(rc.match_ids) matches WHERE matches.value = de.match_id)
     AND md.decision_id = (SELECT newest.decision_id FROM moderator_decisions newest
       WHERE newest.case_id = rc.case_id ORDER BY newest.decided_at DESC, newest.decision_id DESC LIMIT 1)
     AND (md.verdict = 'false_positive' OR EXISTS (
       SELECT 1 FROM json_each(COALESCE(NULLIF(md.detector_feedback,''),'[]')) feedback
       WHERE json_extract(feedback.value,'$.detector_id') = de.detector_id
         AND json_extract(feedback.value,'$.correct') = 'no')))`
