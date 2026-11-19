INSERT INTO cliproxy.request_facts
(request_id,started_at,ingested_at,conversation_id,turn_id,client,model,kind,outcome,status_code,duration_ms,prior_seen,last_user_text,reply_text,msg_direction,msg_position,msg_role,msg_type,msg_text,msg_tool_name,msg_call_id,msg_arguments,msg_is_error,msg_text_hash)
WITH intDiv(number,12) AS session, number%12 AS step,
 toDateTime64('2026-11-18 16:00:00',9,'UTC') - toIntervalSecond((24999-session)*155+12-step) AS ts,
 concat('Please fix the common archive bug ',if(session=24990,'rarelyfoundneedle ',''),repeat(hex(cityHash64(session)),190)) AS prompt,
 arrayMap(i->if(i=0 AND step%3=0,'user',if(i%3=0,'tool','assistant')),range(30)) AS roles
SELECT concat('req-',toString(number)),ts,ts,concat('claude:scale-',toString(session)),'','claude-code','claude-opus-5-5','messages','succeeded',200,1000,step>0,
 prompt,repeat(hex(cityHash64(number)),192),
 arrayMap(i->if(i<15,'input','output'),range(30)),range(30),roles,
 arrayMap(r->if(r='tool','tool_result','message'),roles),
 arrayConcat([prompt],arrayMap(i->repeat(hex(cityHash64(number,i)),6),range(29))),
 arrayMap(i->'',range(30)),arrayMap(i->'',range(30)),arrayMap(i->'',range(30)),arrayMap(i->false,range(30)),arrayMap(i->cityHash64(number,i),range(30))
FROM numbers(300000);
INSERT INTO cliproxy.requests (request_id,trace_id,started_at,completed_at,model,request_body,response_body)
SELECT request_id,request_id,started_at,addSeconds(started_at,1),model,
 concat('{"input":"',repeat(hex(cityHash64(request_id)),if(endsWith(request_id,'42'),16000,1)),'"}'),'{"output":"done"}'
FROM cliproxy.request_facts;
INSERT INTO cliproxy.usage (request_id,trace_id,requested_at,model,api_key_hash,input_tokens,output_tokens,total_tokens)
SELECT request_id,request_id,addMilliseconds(started_at,100),model,'scale-key',1000,100,1100 FROM cliproxy.request_facts;
