using Jellyfin.Plugin.JAVBeacon.Tasks;
using MediaBrowser.Controller;
using MediaBrowser.Controller.Plugins;
using MediaBrowser.Model.Tasks;
using Microsoft.Extensions.DependencyInjection;

namespace Jellyfin.Plugin.JAVBeacon;

public sealed class PluginServiceRegistrator : IPluginServiceRegistrator
{
    public void RegisterServices(IServiceCollection services, IServerApplicationHost applicationHost)
    {
        services.AddHttpClient(nameof(JAVBeaconClient));
        services.AddSingleton<JAVBeaconClient>();
        services.AddSingleton<WatchedStatusSynchronizer>();
        services.AddHostedService<PlaybackBridge>();
        // Registered as a singleton (not just AddHostedService, which would
        // only expose it as IHostedService) so SyncCollectionsTask can also
        // resolve and call the same instance's RunFullSyncAsync for its
        // scheduled catch-up pass.
        services.AddSingleton<LibrarySyncService>();
        services.AddHostedService(sp => sp.GetRequiredService<LibrarySyncService>());
        services.AddScoped<IScheduledTask, SyncWatchedStatusTask>();
        services.AddScoped<IScheduledTask, SyncCollectionsTask>();
    }
}
