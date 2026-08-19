local ttl = tonumber(ARGV[2])
if ttl and ttl > 0 then redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl) else redis.call('SET', KEYS[1], ARGV[1]) end
if ARGV[3] ~= '' then
  redis.call('HSET', KEYS[2], ARGV[3], ARGV[4])
end
if ttl and ttl > 0 then redis.call('PEXPIRE', KEYS[2], ttl) else redis.call('PERSIST', KEYS[2]) end
return 1
